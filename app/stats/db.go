package stats

import (
	"database/sql"
	"os"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
)

const DefaultIntensity = 50

type DistortionerDB struct {
	db     *sql.DB
	insert *sql.Stmt
	logger *zap.SugaredLogger
}

type Stat struct {
	Interactions int
	Chats        int
	Groups       int
	Sticker      int
	Animation    int
	Video        int
	VideoNote    int
	Voice        int
	Photo        int
	Text         int
}

type Period string

const (
	Daily   Period = "-1 day"
	Weekly  Period = "-7 days"
	Monthly Period = "-1 month"
)

const statQuery = `
	select
		   count(*) as interactions,
		   count(distinct(user_id)) as users,
		   count(distinct (case when is_group_chat = 1 then user_id end)) as groups,
		   count(case when type = 'sticker' then type end) as sticker,
		   count(case when type = 'animation' then type end) as animation,
		   count(case when type = 'video' then type end) as video,
		   count(case when type = 'videonote' then type end) as videonote,
		   count(case when type = 'voice' then type end) as voice,
		   count(case when type = 'photo' then type end) as photo,
		   count(case when type = 'text' then type end) as text
	from stats
	where date >= datetime('now', ?, 'localtime') and datetime('now','localtime');
`

func InitDB(logger *zap.SugaredLogger) *DistortionerDB {
	err := os.Mkdir("data", os.ModePerm)
	if err != nil && !os.IsExist(err) {
		logger.Fatal("Failed to create data directory for stat DB", err)
	}
	db, err := sql.Open("sqlite3", "file:data/distortioner.sqlite?cache=shared")
	if err != nil {
		logger.Fatal(err)
	}
	dist := DistortionerDB{
		db:     db,
		logger: logger,
	}
	db.SetMaxOpenConns(1)

	_, err = db.Exec(`create table if not exists stats(id integer not null primary key, user_id integer, is_group_chat integer, date integer, type text);`)
	if err != nil {
		logger.Fatal(err)
	}
	_, err = db.Exec(`create index if not exists dateidx on stats(date asc);`)
	if err != nil {
		logger.Fatal(err)
	}
	insertStat, err := db.Prepare(`insert into stats(user_id, is_group_chat, date, type) values(?, ?, ?, ?);`)
	if err != nil {
		logger.Fatal(err)
	}
	dist.insert = insertStat

	_, err = db.Exec(`create table if not exists user_settings(user_id integer not null primary key, intensity integer not null default 50)`)
	if err != nil {
		logger.Fatal(err)
	}
	// Progressive /ramp endpoints; NULL from/to = never configured (defaults derived from intensity on enable).
	_, err = db.Exec(`alter table user_settings add column ramp_from integer`)
	if err != nil && !isSQLiteDuplicateColumn(err) {
		logger.Fatal(err)
	}
	_, err = db.Exec(`alter table user_settings add column ramp_to integer`)
	if err != nil && !isSQLiteDuplicateColumn(err) {
		logger.Fatal(err)
	}
	_, err = db.Exec(`alter table user_settings add column ramp_enabled integer not null default 0`)
	if err == nil {
		// Column just added: previous schema treated non-null endpoints as enabled.
		_, _ = db.Exec(`update user_settings set ramp_enabled = 1 where ramp_from is not null and ramp_to is not null`)
	} else if !isSQLiteDuplicateColumn(err) {
		logger.Fatal(err)
	}
	_, err = db.Exec(`create table if not exists banned_users(user_id integer not null primary key, banned_at text not null)`)
	if err != nil {
		logger.Fatal(err)
	}

	return &dist
}

func isSQLiteDuplicateColumn(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column")
}

func (d *DistortionerDB) GetUserIntensity(userID int64) int {
	var intensity sql.NullInt64
	err := d.db.QueryRow(`select intensity from user_settings where user_id = ?`, userID).Scan(&intensity)
	if err != nil || !intensity.Valid {
		return DefaultIntensity
	}
	v := int(intensity.Int64)
	if v < 1 || v > 100 {
		return DefaultIntensity
	}
	return v
}

func (d *DistortionerDB) SetUserIntensity(userID int64, intensity int) error {
	if intensity < 1 {
		intensity = 1
	}
	if intensity > 100 {
		intensity = 100
	}
	_, err := d.db.Exec(`
		insert into user_settings(user_id, intensity) values(?, ?)
		on conflict(user_id) do update set intensity = excluded.intensity`,
		userID, intensity)
	return err
}

// GetUserRamp returns progressive from→to when ramp is enabled for this user.
func (d *DistortionerDB) GetUserRamp(userID int64) (from, to int, ok bool) {
	var rampFrom, rampTo sql.NullInt64
	var enabled sql.NullInt64
	err := d.db.QueryRow(`select ramp_from, ramp_to, ramp_enabled from user_settings where user_id = ?`, userID).Scan(&rampFrom, &rampTo, &enabled)
	if err != nil || !enabled.Valid || enabled.Int64 == 0 {
		return 0, 0, false
	}
	from, to = int(rampFrom.Int64), int(rampTo.Int64)
	if !rampFrom.Valid || !rampTo.Valid || from < 1 || from > 100 || to < 1 || to > 100 {
		return 0, 0, false
	}
	return from, to, true
}

// GetUserRampConfig returns saved endpoints (even if disabled) and whether ramp is on.
func (d *DistortionerDB) GetUserRampConfig(userID int64) (from, to int, enabled bool, hasEndpoints bool) {
	var rampFrom, rampTo sql.NullInt64
	var en sql.NullInt64
	err := d.db.QueryRow(`select ramp_from, ramp_to, ramp_enabled from user_settings where user_id = ?`, userID).Scan(&rampFrom, &rampTo, &en)
	if err != nil {
		return 0, 0, false, false
	}
	enabled = en.Valid && en.Int64 != 0
	if rampFrom.Valid && rampTo.Valid {
		from, to = int(rampFrom.Int64), int(rampTo.Int64)
		if from >= 1 && from <= 100 && to >= 1 && to <= 100 {
			return from, to, enabled, true
		}
	}
	return 0, 0, enabled, false
}

func DefaultRampEndpoints(intensity int) (from, to int) {
	to = intensity
	if to < 1 {
		to = DefaultIntensity
	}
	if to > 100 {
		to = 100
	}
	from = to * 2 / 5
	if from < 1 {
		from = 1
	}
	return from, to
}

func (d *DistortionerDB) SetUserRamp(userID int64, from, to int) error {
	if from < 1 {
		from = 1
	}
	if from > 100 {
		from = 100
	}
	if to < 1 {
		to = 1
	}
	if to > 100 {
		to = 100
	}
	_, err := d.db.Exec(`
		insert into user_settings(user_id, intensity, ramp_from, ramp_to, ramp_enabled) values(?, ?, ?, ?, 1)
		on conflict(user_id) do update set ramp_from = excluded.ramp_from, ramp_to = excluded.ramp_to, ramp_enabled = 1`,
		userID, DefaultIntensity, from, to)
	return err
}

// ClearUserRamp disables progressive mode but keeps saved endpoints for the next toggle.
func (d *DistortionerDB) ClearUserRamp(userID int64) error {
	_, err := d.db.Exec(`
		insert into user_settings(user_id, intensity, ramp_enabled) values(?, ?, 0)
		on conflict(user_id) do update set ramp_enabled = 0`,
		userID, DefaultIntensity)
	return err
}

// ToggleUserRamp flips progressive on/off. When turning on without saved endpoints, uses defaults.
func (d *DistortionerDB) ToggleUserRamp(userID int64) (from, to int, enabled bool, err error) {
	from, to, wasOn, has := d.GetUserRampConfig(userID)
	if wasOn {
		err = d.ClearUserRamp(userID)
		return from, to, false, err
	}
	if !has {
		from, to = DefaultRampEndpoints(d.GetUserIntensity(userID))
	}
	err = d.SetUserRamp(userID, from, to)
	return from, to, true, err
}

func (d *DistortionerDB) IsBanned(userID int64) bool {
	var id int64
	err := d.db.QueryRow(`select user_id from banned_users where user_id = ?`, userID).Scan(&id)
	return err == nil
}

// BanUser persists a ban. Returns true if newly banned.
func (d *DistortionerDB) BanUser(userID int64) (bool, error) {
	if d.IsBanned(userID) {
		return false, nil
	}
	_, err := d.db.Exec(`insert into banned_users(user_id, banned_at) values(?, ?)`, userID, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return false, err
	}
	return true, nil
}

// UnbanUser removes a ban. Returns true if the user was banned.
func (d *DistortionerDB) UnbanUser(userID int64) (bool, error) {
	res, err := d.db.Exec(`delete from banned_users where user_id = ?`, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (d *DistortionerDB) BannedUsers() ([]int64, error) {
	rows, err := d.db.Query(`select user_id from banned_users order by banned_at desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (d *DistortionerDB) SaveStat(message *tb.Message, isCommand bool) {
	if message == nil {
		return
	}
	if isCommand {
		d.SaveStat(message.ReplyTo, false)
		return
	}
	messageType := "text"
	switch {
	case message.Animation != nil:
		messageType = "animation"
	case message.Video != nil:
		messageType = "video"
	case message.VideoNote != nil:
		messageType = "videonote"
	case message.Voice != nil:
		messageType = "voice"
	case message.Sticker != nil:
		messageType = "sticker"
	case message.Photo != nil:
		messageType = "photo"
	}
	_, err := d.insert.Exec(message.Chat.ID, message.FromGroup(), time.Now(), messageType)
	if err != nil {
		d.logger.Error(err)
	}
}

func (d *DistortionerDB) GetStat(period Period) (Stat, error) {
	row := d.db.QueryRow(statQuery, period)
	var stat Stat
	err := row.Scan(&stat.Interactions, &stat.Chats, &stat.Groups, &stat.Sticker, &stat.Animation, &stat.Video,
		&stat.VideoNote, &stat.Voice, &stat.Photo, &stat.Text)
	return stat, err
}

func (d *DistortionerDB) Close() {
	d.db.Close()
}
