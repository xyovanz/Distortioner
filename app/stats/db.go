package stats

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

	// In-memory mirror of banned_users (user_id → reason) so the per-update ban check skips SQLite.
	bansMu sync.RWMutex
	bans   map[int64]string

	// Last users-table write per user, so TouchUser doesn't write on every message.
	touchMu sync.Mutex
	touched map[int64]touchState

	// Stats rows are queued and written in batches by statWriter.
	statsMu   sync.RWMutex // guards statsDone against SaveStat after Close
	statsDone bool
	statRows  chan statRow
	flushReq  chan chan struct{}
	writerEnd chan struct{}
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
	Users        int // distinct senders (only rows recorded since sender_id was added)
	NewUsers     int // users first seen in the period
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
		   count(case when type = 'text' then type end) as text,
		   count(distinct(sender_id)) as senders
	from stats
	where date >= datetime('now', ?, 'localtime');
`

func InitDB(logger *zap.SugaredLogger) *DistortionerDB {
	return openDB(logger, filepath.Join("data", "distortioner.sqlite"))
}

func openDB(logger *zap.SugaredLogger, path string) *DistortionerDB {
	err := os.MkdirAll(filepath.Dir(path), os.ModePerm)
	if err != nil {
		logger.Fatal("Failed to create data directory for stat DB", err)
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?cache=shared&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		logger.Fatal(err)
	}
	dist := DistortionerDB{
		db:        db,
		logger:    logger,
		touched:   make(map[int64]touchState),
		statRows:  make(chan statRow, statQueueSize),
		flushReq:  make(chan chan struct{}),
		writerEnd: make(chan struct{}),
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
	// sender_id is the Telegram user behind the request; user_id has always held the chat ID.
	_, err = db.Exec(`alter table stats add column sender_id integer`)
	if err != nil && !isSQLiteDuplicateColumn(err) {
		logger.Fatal(err)
	}
	_, err = db.Exec(`create index if not exists sender_date_idx on stats(sender_id, date)`)
	if err != nil {
		logger.Fatal(err)
	}
	insertStat, err := db.Prepare(`insert into stats(user_id, sender_id, is_group_chat, date, type) values(?, ?, ?, ?, ?);`)
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
	_, err = db.Exec(`alter table user_settings add column sound_mode text not null default 'distort'`)
	if err != nil && !isSQLiteDuplicateColumn(err) {
		logger.Fatal(err)
	}
	_, err = db.Exec(`create table if not exists banned_users(user_id integer not null primary key, banned_at text not null)`)
	if err != nil {
		logger.Fatal(err)
	}
	if err = dist.migrateUsers(); err != nil {
		logger.Fatal(err)
	}
	if err = dist.loadBans(); err != nil {
		logger.Fatal(err)
	}
	go dist.statWriter()

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

// Settings is everything a distortion needs from user_settings, read in one query.
type Settings struct {
	Intensity int
	RampFrom  int // 0 when /ramp is off
	RampTo    int
	SoundMode string // raw stored value; callers normalize it
}

func (d *DistortionerDB) GetUserSettings(userID int64) Settings {
	s := Settings{Intensity: DefaultIntensity}
	var intensity, rampFrom, rampTo, rampEnabled sql.NullInt64
	var mode sql.NullString
	err := d.db.QueryRow(`select intensity, ramp_from, ramp_to, ramp_enabled, sound_mode from user_settings where user_id = ?`, userID).
		Scan(&intensity, &rampFrom, &rampTo, &rampEnabled, &mode)
	if err != nil {
		return s
	}
	if v := int(intensity.Int64); intensity.Valid && v >= 1 && v <= 100 {
		s.Intensity = v
	}
	from, to := int(rampFrom.Int64), int(rampTo.Int64)
	if rampEnabled.Int64 != 0 && rampFrom.Valid && rampTo.Valid && from >= 1 && from <= 100 && to >= 1 && to <= 100 {
		s.RampFrom, s.RampTo = from, to
	}
	s.SoundMode = mode.String
	return s
}

// SetUserSoundMode stores an already validated mode.
func (d *DistortionerDB) SetUserSoundMode(userID int64, mode string) error {
	_, err := d.db.Exec(`
		insert into user_settings(user_id, intensity, sound_mode) values(?, ?, ?)
		on conflict(user_id) do update set sound_mode = excluded.sound_mode`,
		userID, DefaultIntensity, mode)
	return err
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

// SaveStat queues one distortion request for the batch writer. For a /distort reply the media
// type comes from the replied-to message, but the sender is whoever issued the command.
func (d *DistortionerDB) SaveStat(message *tb.Message, isCommand bool) {
	if message == nil {
		return
	}
	sender := message.Sender
	if isCommand {
		message = message.ReplyTo
		if message == nil {
			return
		}
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
	row := statRow{chatID: message.Chat.ID, isGroup: message.FromGroup(), at: time.Now(), kind: messageType}
	if sender != nil {
		row.senderID = sender.ID
	}
	d.enqueueStat(row)
}

func (d *DistortionerDB) GetStat(period Period) (Stat, error) {
	row := d.db.QueryRow(statQuery, period)
	var stat Stat
	err := row.Scan(&stat.Interactions, &stat.Chats, &stat.Groups, &stat.Sticker, &stat.Animation, &stat.Video,
		&stat.VideoNote, &stat.Voice, &stat.Photo, &stat.Text, &stat.Users)
	if err != nil {
		return stat, err
	}
	stat.NewUsers, err = d.NewUsers(period)
	return stat, err
}

func (d *DistortionerDB) Close() {
	d.stopStatWriter()
	d.db.Close()
}
