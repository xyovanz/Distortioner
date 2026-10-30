package stats

import (
	"database/sql"
	"strings"
	"time"

	tb "gopkg.in/telebot.v3"
)

// Ban log actions.
const (
	ActionBan           = "ban"
	ActionUnban         = "unban"
	ActionUnbanRequest  = "unban_request"
	ActionBanReasonEdit = "ban_reason"
)

type User struct {
	ID        int64
	Username  string
	FirstName string
	LastName  string
	FirstSeen time.Time
	LastSeen  time.Time
}

// DisplayName returns "First Last (@username)" with whatever parts are known.
func (u User) DisplayName() string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	switch {
	case name != "" && u.Username != "":
		return name + " (@" + u.Username + ")"
	case u.Username != "":
		return "@" + u.Username
	case name != "":
		return name
	default:
		return "Unknown"
	}
}

type Ban struct {
	UserID   int64
	Reason   string
	BannedAt time.Time
	BannedBy int64
	User     User // zero value when the user was never seen
}

type BanEvent struct {
	UserID  int64
	Action  string
	Reason  string
	AdminID int64
	At      time.Time
}

type UserStat struct {
	Total     int
	Sticker   int
	Animation int
	Video     int
	VideoNote int
	Voice     int
	Photo     int
	Text      int
}

type UserCount struct {
	User  User
	Count int
}

func (d *DistortionerDB) migrateUsers() error {
	stmts := []string{
		`create table if not exists users(
			user_id integer not null primary key,
			username text not null default '',
			first_name text not null default '',
			last_name text not null default '',
			first_seen text not null,
			last_seen text not null)`,
		`create index if not exists users_username_idx on users(username collate nocase)`,
		`create index if not exists users_first_seen_idx on users(first_seen)`,
		`create table if not exists ban_log(
			id integer not null primary key,
			user_id integer not null,
			action text not null,
			reason text not null default '',
			admin_id integer not null default 0,
			at text not null)`,
		`create index if not exists ban_log_user_idx on ban_log(user_id, at)`,
	}
	for _, q := range stmts {
		if _, err := d.db.Exec(q); err != nil {
			return err
		}
	}
	for _, q := range []string{
		`alter table banned_users add column reason text not null default ''`,
		`alter table banned_users add column banned_by integer not null default 0`,
	} {
		if _, err := d.db.Exec(q); err != nil && !isSQLiteDuplicateColumn(err) {
			return err
		}
	}
	return nil
}

func (d *DistortionerDB) loadBans() error {
	rows, err := d.db.Query(`select user_id, reason from banned_users`)
	if err != nil {
		return err
	}
	defer rows.Close()
	bans := make(map[int64]string)
	for rows.Next() {
		var id int64
		var reason string
		if err := rows.Scan(&id, &reason); err != nil {
			return err
		}
		bans[id] = reason
	}
	d.bansMu.Lock()
	d.bans = bans
	d.bansMu.Unlock()
	return rows.Err()
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// periodStartUTC converts a Period to the RFC3339 UTC lower bound used by users/ban_log columns.
const periodStartUTC = `strftime('%Y-%m-%dT%H:%M:%SZ', 'now', ?)`

// touchInterval is how stale users.last_seen may get before TouchUser writes again.
const touchInterval = 10 * time.Minute

type touchState struct {
	at    time.Time
	names string
}

// shouldTouch reports whether the user row needs a write: first sight since start, a name
// change, or last_seen older than touchInterval. Records the write when it says yes.
func (d *DistortionerDB) shouldTouch(u *tb.User, now time.Time) bool {
	names := u.Username + "\x00" + u.FirstName + "\x00" + u.LastName
	d.touchMu.Lock()
	defer d.touchMu.Unlock()
	if prev, ok := d.touched[u.ID]; ok && prev.names == names && now.Sub(prev.at) < touchInterval {
		return false
	}
	d.touched[u.ID] = touchState{at: now, names: names}
	return true
}

// TouchUser records that the user was seen now, refreshing their names.
func (d *DistortionerDB) TouchUser(u *tb.User) {
	if u == nil || u.IsBot || !d.shouldTouch(u, time.Now()) {
		return
	}
	now := nowUTC()
	_, err := d.db.Exec(`
		insert into users(user_id, username, first_name, last_name, first_seen, last_seen) values(?, ?, ?, ?, ?, ?)
		on conflict(user_id) do update set
			username = excluded.username, first_name = excluded.first_name,
			last_name = excluded.last_name, last_seen = excluded.last_seen`,
		u.ID, u.Username, u.FirstName, u.LastName, now, now)
	if err != nil {
		d.logger.Error(err)
	}
}

const userColumns = `user_id, username, first_name, last_name, first_seen, last_seen`

func scanUser(scan func(...any) error) (User, error) {
	var u User
	var first, last string
	err := scan(&u.ID, &u.Username, &u.FirstName, &u.LastName, &first, &last)
	u.FirstSeen, u.LastSeen = parseTime(first), parseTime(last)
	return u, err
}

func (d *DistortionerDB) GetUser(userID int64) (User, bool) {
	u, err := scanUser(d.db.QueryRow(`select `+userColumns+` from users where user_id = ?`, userID).Scan)
	return u, err == nil
}

// FindUserByUsername looks a user up by @username (case-insensitive, leading @ optional).
func (d *DistortionerDB) FindUserByUsername(username string) (User, bool) {
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if username == "" {
		return User{}, false
	}
	u, err := scanUser(d.db.QueryRow(`select `+userColumns+` from users where username = ? collate nocase
		order by last_seen desc limit 1`, username).Scan)
	return u, err == nil
}

func (d *DistortionerDB) TotalUsers() (int, error) {
	var n int
	err := d.db.QueryRow(`select count(*) from users`).Scan(&n)
	return n, err
}

func (d *DistortionerDB) NewUsers(period Period) (int, error) {
	var n int
	err := d.db.QueryRow(`select count(*) from users where first_seen >= `+periodStartUTC, string(period)).Scan(&n)
	return n, err
}

const userStatColumns = `
	count(*),
	count(case when type = 'sticker' then type end),
	count(case when type = 'animation' then type end),
	count(case when type = 'video' then type end),
	count(case when type = 'videonote' then type end),
	count(case when type = 'voice' then type end),
	count(case when type = 'photo' then type end),
	count(case when type = 'text' then type end)`

// GetUserStat counts requests by this sender in the period; an empty period means all time.
func (d *DistortionerDB) GetUserStat(userID int64, period Period) (UserStat, error) {
	q := `select ` + userStatColumns + ` from stats where sender_id = ?`
	args := []any{userID}
	if period != "" {
		q += ` and date >= datetime('now', ?, 'localtime')`
		args = append(args, string(period))
	}
	var s UserStat
	err := d.db.QueryRow(q, args...).Scan(&s.Total, &s.Sticker, &s.Animation, &s.Video, &s.VideoNote, &s.Voice, &s.Photo, &s.Text)
	return s, err
}

// TopUsers returns the most active senders in the period.
func (d *DistortionerDB) TopUsers(period Period, limit int) ([]UserCount, error) {
	rows, err := d.db.Query(`
		select s.sender_id, coalesce(u.username, ''), coalesce(u.first_name, ''), coalesce(u.last_name, ''),
		       coalesce(u.first_seen, ''), coalesce(u.last_seen, ''), s.n
		from (select sender_id, count(*) as n from stats
		      where sender_id is not null and date >= datetime('now', ?, 'localtime')
		      group by sender_id) s
		left join users u on u.user_id = s.sender_id
		order by s.n desc, s.sender_id
		limit ?`, string(period), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserCount
	for rows.Next() {
		var uc UserCount
		var first, last string
		if err := rows.Scan(&uc.User.ID, &uc.User.Username, &uc.User.FirstName, &uc.User.LastName, &first, &last, &uc.Count); err != nil {
			return nil, err
		}
		uc.User.FirstSeen, uc.User.LastSeen = parseTime(first), parseTime(last)
		out = append(out, uc)
	}
	return out, rows.Err()
}

func (d *DistortionerDB) IsBanned(userID int64) bool {
	_, ok := d.BanReason(userID)
	return ok
}

// BanReason returns the stored reason (possibly empty) and whether the user is banned.
func (d *DistortionerDB) BanReason(userID int64) (string, bool) {
	d.bansMu.RLock()
	defer d.bansMu.RUnlock()
	reason, ok := d.bans[userID]
	return reason, ok
}

func (d *DistortionerDB) logBanEvent(tx *sql.Tx, userID int64, action, reason string, adminID int64, at string) error {
	_, err := tx.Exec(`insert into ban_log(user_id, action, reason, admin_id, at) values(?, ?, ?, ?, ?)`,
		userID, action, reason, adminID, at)
	return err
}

func (d *DistortionerDB) inTx(f func(tx *sql.Tx) error) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	if err := f(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// BanUser persists a ban. Returns true if newly banned. Banning an already banned user
// with a non-empty reason replaces the reason.
func (d *DistortionerDB) BanUser(userID, adminID int64, reason string) (bool, error) {
	reason = strings.TrimSpace(reason)
	old, banned := d.BanReason(userID)
	if banned && (reason == "" || reason == old) {
		return false, nil
	}
	now := nowUTC()
	err := d.inTx(func(tx *sql.Tx) error {
		if banned {
			if _, err := tx.Exec(`update banned_users set reason = ? where user_id = ?`, reason, userID); err != nil {
				return err
			}
			return d.logBanEvent(tx, userID, ActionBanReasonEdit, reason, adminID, now)
		}
		if _, err := tx.Exec(`insert into banned_users(user_id, banned_at, reason, banned_by) values(?, ?, ?, ?)
			on conflict(user_id) do update set reason = excluded.reason`, userID, now, reason, adminID); err != nil {
			return err
		}
		return d.logBanEvent(tx, userID, ActionBan, reason, adminID, now)
	})
	if err != nil {
		return false, err
	}
	d.bansMu.Lock()
	d.bans[userID] = reason
	d.bansMu.Unlock()
	return !banned, nil
}

// UnbanUser removes a ban. Returns true if the user was banned.
func (d *DistortionerDB) UnbanUser(userID, adminID int64, reason string) (bool, error) {
	var removed bool
	err := d.inTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`delete from banned_users where user_id = ?`, userID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if removed = n > 0; !removed {
			return nil
		}
		return d.logBanEvent(tx, userID, ActionUnban, strings.TrimSpace(reason), adminID, nowUTC())
	})
	if err != nil {
		return false, err
	}
	d.bansMu.Lock()
	delete(d.bans, userID)
	d.bansMu.Unlock()
	return removed, nil
}

// LogUnbanRequest stores the user's /unbanrequest message in their ban history.
func (d *DistortionerDB) LogUnbanRequest(userID int64, message string) error {
	return d.inTx(func(tx *sql.Tx) error {
		return d.logBanEvent(tx, userID, ActionUnbanRequest, strings.TrimSpace(message), 0, nowUTC())
	})
}

func (d *DistortionerDB) GetBan(userID int64) (Ban, bool) {
	var b Ban
	var at string
	err := d.db.QueryRow(`select user_id, reason, banned_at, banned_by from banned_users where user_id = ?`, userID).
		Scan(&b.UserID, &b.Reason, &at, &b.BannedBy)
	if err != nil {
		return Ban{}, false
	}
	b.BannedAt = parseTime(at)
	b.User, _ = d.GetUser(userID)
	return b, true
}

func (d *DistortionerDB) BannedUsers() ([]Ban, error) {
	rows, err := d.db.Query(`
		select b.user_id, b.reason, b.banned_at, b.banned_by,
		       coalesce(u.username, ''), coalesce(u.first_name, ''), coalesce(u.last_name, '')
		from banned_users b left join users u on u.user_id = b.user_id
		order by b.banned_at desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ban
	for rows.Next() {
		var b Ban
		var at string
		if err := rows.Scan(&b.UserID, &b.Reason, &at, &b.BannedBy, &b.User.Username, &b.User.FirstName, &b.User.LastName); err != nil {
			return nil, err
		}
		b.BannedAt = parseTime(at)
		b.User.ID = b.UserID
		out = append(out, b)
	}
	return out, rows.Err()
}

// BanHistory returns the latest ban/unban events for the user, newest first.
func (d *DistortionerDB) BanHistory(userID int64, limit int) ([]BanEvent, error) {
	rows, err := d.db.Query(`select user_id, action, reason, admin_id, at from ban_log
		where user_id = ? order by at desc, id desc limit ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BanEvent
	for rows.Next() {
		var e BanEvent
		var at string
		if err := rows.Scan(&e.UserID, &e.Action, &e.Reason, &e.AdminID, &at); err != nil {
			return nil, err
		}
		e.At = parseTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}
