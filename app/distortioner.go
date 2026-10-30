package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
	"gopkg.in/telebot.v3/middleware"

	"github.com/graynk/distortioner/distorters"
	"github.com/graynk/distortioner/stats"
	"github.com/graynk/distortioner/tools"
)

const (
	MaxSizeMb = 20_000_000
)

var safeBranchRe = regexp.MustCompile(`^[a-zA-Z0-9._/\-]+$`)

type DistorterBot struct {
	adminID     int64
	rl          *tools.RateLimiter
	logger      *zap.SugaredLogger
	graceWg     *sync.WaitGroup
	videoWorker *tools.VideoWorker
	codec       string
	db          *stats.DistortionerDB
	startedAt   time.Time

	bannedNotice  *tools.Throttle // "you are banned" replies, once per hour per user
	unbanRequests *tools.Throttle // /unbanrequest, once per day per user
	chatRights    *chatRightsCache
}

// mediaSettings reads the user's distortion settings in one query, with the sound mode normalized.
func (d DistorterBot) mediaSettings(userID int64) stats.Settings {
	s := d.db.GetUserSettings(userID)
	s.SoundMode = distorters.NormalizeSoundMode(s.SoundMode)
	return s
}

func (d DistorterBot) withSettings(h func(tb.Context, stats.Settings) error) tb.HandlerFunc {
	return func(c tb.Context) error {
		m := c.Message()
		if m == nil || m.Sender == nil {
			return nil
		}
		return h(c, d.mediaSettings(m.Sender.ID))
	}
}

func failStatus(err error, progress *tb.Message) string {
	if progress != nil && distorters.IsFailureStatus(progress.Text) {
		return progress.Text
	}
	return distorters.UserFacingError(err)
}

// download fetches the message's media into the temp dir and tells the user if that failed.
func (d DistorterBot) download(c tb.Context) (string, error) {
	filename, err := tools.JustGetTheFile(c.Bot(), c.Message())
	if err != nil {
		d.logger.Error(err)
		d.SendMessageWithRepeater(c, distorters.FailedDownload)
	}
	return filename, err
}

// submitVideoJob gates size/duration/rate-limit, enqueues work, and tells the user their queue
// position when busy. maxDuration 0 skips the duration check (stickers carry no duration).
func (d DistorterBot) submitVideoJob(c tb.Context, fileSize int64, duration, maxDuration int, work func()) error {
	m := c.Message()
	if fileSize > MaxSizeMb {
		return d.SendMessageWithRepeater(c, distorters.TooBig)
	}
	// Telegram reports the duration up front, so overlong media is rejected before it is
	// downloaded or takes a queue slot. DistortVideo still checks with ffprobe.
	if maxDuration > 0 && duration > maxDuration {
		return d.SendMessageWithRepeater(c, distorters.TooLong)
	}
	if rate, diff := d.rl.GetRateOverPeriod(m.Chat.ID, time.Now().Unix()); rate > tools.AllowedOverTime {
		return d.SendMessageWithRepeater(c, tools.FormatRateLimitResponse(diff))
	}
	pos, err := d.videoWorker.Submit(m.Chat.ID, work)
	if err != nil {
		d.SendMessageWithRepeater(c, err.Error())
		return nil
	}
	if d.videoWorker.IsBusy() {
		d.SendMessageWithRepeater(c, distorters.FormatQueued(pos))
	}
	return nil
}

// handleVideoLike queues a GIF, video or video note; wrap builds the reply from the output file.
func (d DistorterBot) handleVideoLike(c tb.Context, s stats.Settings, fileSize int64, duration int, withAudio bool, wrap func(output string) interface{}) error {
	b := c.Bot()
	return d.submitVideoJob(c, fileSize, duration, distorters.MaxVideoSeconds, func() {
		progressMessage, filename, output, err := d.HandleAnimationCommon(c, s, withAudio)
		failed := err != nil
		defer tools.RemoveTemp(filename, failed)
		defer tools.RemoveTemp(output, failed)
		if failed {
			d.logger.Error(err)
			if progressMessage != nil && progressMessage.Text != distorters.TooLong {
				d.DoneMessageWithRepeater(b, progressMessage, failStatus(err, progressMessage))
			}
			return
		}
		err = d.SendMessageWithRepeater(c, wrap(output))
		d.DoneMessageWithRepeater(b, progressMessage, "")
		if err != nil {
			d.logger.Error(err)
		}
	})
}

func (d DistorterBot) handleAnimationDistortion(c tb.Context, s stats.Settings) error {
	m := c.Message()
	return d.handleVideoLike(c, s, m.Animation.FileSize, m.Animation.Duration, false, func(output string) interface{} {
		distorted := &tb.Animation{File: tb.FromDisk(output), FileName: filepath.Base(output)}
		if m.Caption != "" {
			distorted.Caption = distorters.DistortText(m.Caption, s.Intensity)
		}
		return distorted
	})
}

func (d DistorterBot) handleVideoDistortion(c tb.Context, s stats.Settings) error {
	m := c.Message()
	return d.handleVideoLike(c, s, m.Video.FileSize, m.Video.Duration, true, func(output string) interface{} {
		return &tb.Video{File: tb.FromDisk(output)}
	})
}

func (d DistorterBot) handleVideoNoteDistortion(c tb.Context, s stats.Settings) error {
	m := c.Message()
	return d.handleVideoLike(c, s, m.VideoNote.FileSize, m.VideoNote.Duration, true, func(output string) interface{} {
		return &tb.VideoNote{File: tb.FromDisk(output)}
	})
}

// handleImageDistortion distorts a photo or static sticker; wrap builds the reply.
func (d DistorterBot) handleImageDistortion(c tb.Context, s stats.Settings, wrap func(file tb.File) interface{}) error {
	filename, err := d.download(c)
	if err != nil {
		return err
	}
	err = distorters.DistortImage(filename, s.Intensity)
	failed := err != nil
	defer tools.RemoveTemp(filename, failed)
	if failed {
		d.SendMessageWithRepeater(c, distorters.FailedDistortImage)
		return err
	}
	return d.SendMessageWithRepeater(c, wrap(tb.FromDisk(filename)))
}

func (d DistorterBot) handlePhotoDistortion(c tb.Context, s stats.Settings) error {
	m := c.Message()
	return d.handleImageDistortion(c, s, func(file tb.File) interface{} {
		distorted := &tb.Photo{File: file}
		if m.Caption != "" {
			distorted.Caption = distorters.DistortText(m.Caption, s.Intensity)
		}
		return distorted
	})
}

func (d DistorterBot) handleVideoStickerDistortion(c tb.Context, s stats.Settings) error {
	m := c.Message()
	return d.submitVideoJob(c, m.Sticker.FileSize, 0, 0, func() {
		filename, output, err := d.HandleVideoSticker(c, s)
		failed := err != nil
		defer tools.RemoveTemp(filename, failed)
		defer tools.RemoveTemp(output, failed)
		if failed {
			d.logger.Error(err)
			d.SendMessageWithRepeater(c, distorters.UserFacingError(err))
			return
		}
		distorted := &tb.Sticker{File: tb.FromDisk(output)}
		if sendErr := d.SendMessageWithRepeater(c, distorted); sendErr != nil {
			d.logger.Error(sendErr)
		}
	})
}

func (d DistorterBot) handleStickerDistortion(c tb.Context, s stats.Settings) error {
	m := c.Message()
	switch {
	case m.Sticker.Animated:
		return d.SendMessageWithRepeater(c, AnimatedStickersUnsupported)
	case m.Sticker.Video:
		return d.handleVideoStickerDistortion(c, s)
	default:
		return d.handleImageDistortion(c, s, func(file tb.File) interface{} {
			return &tb.Sticker{File: file}
		})
	}
}

func (d DistorterBot) handleTextDistortion(c tb.Context, s stats.Settings) error {
	m := c.Message()
	if tools.IsBotCommand(m) {
		return nil
	}
	return d.SendMessageWithRepeater(c, distorters.DistortText(c.Text(), s.Intensity))
}

func (d DistorterBot) handleVoiceDistortion(c tb.Context, s stats.Settings) error {
	m := c.Message()
	if m.Voice.FileSize > MaxSizeMb {
		return c.Reply(distorters.TooBig)
	}
	filename, err := d.download(c)
	if err != nil {
		return err
	}
	output := filename + ".ogg"
	err = distorters.DistortSound(filename, output, s.Intensity, s.SoundMode)
	failed := err != nil
	defer tools.RemoveTemp(filename, failed)
	defer tools.RemoveTemp(output, failed)
	if failed {
		d.SendMessageWithRepeater(c, distorters.FailedDistortAudio)
		return err
	}

	distorted := &tb.Voice{File: tb.FromDisk(output)}
	return d.SendMessageWithRepeater(c, distorted)
}

func (d DistorterBot) handleReplyDistortion(c tb.Context) error {
	m := c.Message()
	if m == nil || m.Sender == nil {
		return nil
	}
	if m.ReplyTo == nil {
		msg := "You need to reply with this command to the media you want distorted."
		if m.FromGroup() {
			msg += "\nYou might also need to make chat history visible for new members if your group is private."
		}
		return c.Reply(msg)
	}
	original := m.ReplyTo
	update := c.Update()
	update.Message = original
	tweakedContext := c.Bot().NewContext(update)
	// Settings always come from whoever sent /distort, not from the original author.
	s := d.mediaSettings(m.Sender.ID)
	switch {
	case original.Animation != nil:
		return d.handleAnimationDistortion(tweakedContext, s)
	case original.Sticker != nil:
		return d.handleStickerDistortion(tweakedContext, s)
	case original.Photo != nil:
		return d.handlePhotoDistortion(tweakedContext, s)
	case original.Voice != nil:
		return d.handleVoiceDistortion(tweakedContext, s)
	case original.Video != nil:
		return d.handleVideoDistortion(tweakedContext, s)
	case original.VideoNote != nil:
		return d.handleVideoNoteDistortion(tweakedContext, s)
	case original.Text != "":
		return d.handleTextDistortion(tweakedContext, s)
	}
	return nil
}

func (d DistorterBot) handleIntensity(c tb.Context) error {
	m := c.Message()
	if m == nil || m.Sender == nil {
		return nil
	}
	payload := strings.TrimSpace(m.Payload)
	if payload == "" {
		v := d.db.GetUserIntensity(m.Sender.ID)
		return c.Reply(fmt.Sprintf("Your distortion intensity is %d (1 to 100, default %d). Send /intensity <n> to change it.", v, stats.DefaultIntensity))
	}
	n, err := strconv.Atoi(payload)
	if err != nil || n < 1 || n > 100 {
		return c.Reply("Send a number from 1 (subtle) to 100 (strong), e.g. /intensity 75")
	}
	if err := d.db.SetUserIntensity(m.Sender.ID, n); err != nil {
		d.logger.Error(err)
		return c.Reply("Could not save setting.")
	}
	return c.Reply(fmt.Sprintf("Intensity set to %d.", n))
}

func (d DistorterBot) handleSound(c tb.Context) error {
	m := c.Message()
	if m == nil || m.Sender == nil {
		return nil
	}
	payload := strings.TrimSpace(m.Payload)
	if payload == "" {
		mode := d.mediaSettings(m.Sender.ID).SoundMode
		return c.Reply(fmt.Sprintf("Sound mode: %s. Options: distort (default vibrato), bassboost (max bass). Send /sound <mode>.", mode))
	}
	mode, ok := distorters.ParseSoundMode(payload)
	if !ok {
		return c.Reply("Unknown mode. Use /sound distort or /sound bassboost.")
	}
	if err := d.db.SetUserSoundMode(m.Sender.ID, mode); err != nil {
		d.logger.Error(err)
		return c.Reply("Could not save setting.")
	}
	return c.Reply(fmt.Sprintf("Sound mode set to %s.", mode))
}

func (d DistorterBot) handleRamp(c tb.Context) error {
	m := c.Message()
	if m == nil || m.Sender == nil {
		return nil
	}
	payload := strings.TrimSpace(m.Payload)
	if payload == "" {
		from, to, enabled, err := d.db.ToggleUserRamp(m.Sender.ID)
		if err != nil {
			d.logger.Error(err)
			return c.Reply("Could not save setting.")
		}
		if enabled {
			return c.Reply(fmt.Sprintf("Progressive ramp on: %d → %d across frames. Send /ramp again to turn off, or /ramp <from> <to> to change.", from, to))
		}
		return c.Reply("Progressive ramp off (flat /intensity for every frame). Send /ramp again to turn on.")
	}
	if strings.EqualFold(payload, "off") || strings.EqualFold(payload, "disable") || payload == "0" {
		if err := d.db.ClearUserRamp(m.Sender.ID); err != nil {
			d.logger.Error(err)
			return c.Reply("Could not save setting.")
		}
		return c.Reply("Progressive ramp off.")
	}
	if strings.EqualFold(payload, "on") || strings.EqualFold(payload, "enable") {
		from, to, _, has := d.db.GetUserRampConfig(m.Sender.ID)
		if !has {
			from, to = stats.DefaultRampEndpoints(d.db.GetUserIntensity(m.Sender.ID))
		}
		if err := d.db.SetUserRamp(m.Sender.ID, from, to); err != nil {
			d.logger.Error(err)
			return c.Reply("Could not save setting.")
		}
		return c.Reply(fmt.Sprintf("Progressive ramp on: %d → %d.", from, to))
	}
	parts := strings.Fields(payload)
	if len(parts) != 2 {
		return c.Reply("Usage: /ramp — toggle; /ramp <from> <to> — set endpoints; /ramp off")
	}
	from, err1 := strconv.Atoi(parts[0])
	to, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || from < 1 || from > 100 || to < 1 || to > 100 {
		return c.Reply("Both values must be integers from 1 to 100, e.g. /ramp 20 80")
	}
	if err := d.db.SetUserRamp(m.Sender.ID, from, to); err != nil {
		d.logger.Error(err)
		return c.Reply("Could not save setting.")
	}
	return c.Reply(fmt.Sprintf("Progressive ramp on: %d → %d.", from, to))
}

func (d DistorterBot) handleStart(c tb.Context) error {
	m := c.Message()
	if m == nil || m.Sender == nil {
		return nil
	}
	welcome := "Send me a picture, a sticker, a voice message, a video[note] or a GIF and I'll distort it.\n" +
		"Use /intensity (1–100) for strength, /sound distort|bassboost for audio, /ramp to toggle progressive frames."
	if m.Sender.ID == d.adminID {
		return c.Reply(welcome + "\n\n(admin online)")
	}
	if err := c.Reply(welcome); err != nil {
		d.logger.Error(err)
	}
	username := "N/A"
	if m.Sender.Username != "" {
		username = "@" + m.Sender.Username
	}
	fullName := m.Sender.FirstName
	if m.Sender.LastName != "" {
		fullName += " " + m.Sender.LastName
	}
	if fullName == "" {
		fullName = "Unknown"
	}
	adminText := fmt.Sprintf("АЛЯРМ! До бота доебался\n\nID: %d\nИмя: %s\nUsername: %s", m.Sender.ID, fullName, username)
	markup := userAdminMarkup(m.Sender.ID, d.db.IsBanned(m.Sender.ID))
	if _, err := c.Bot().Send(&tb.User{ID: d.adminID}, adminText, markup); err != nil {
		d.logger.Errorw("admin start alert failed", "err", err)
	}
	if _, err := c.Bot().Forward(&tb.User{ID: d.adminID}, m); err != nil {
		d.logger.Errorw("forward /start failed", "err", err)
	}
	return nil
}

func (d DistorterBot) handleStatus(c tb.Context) error {
	m := c.Message()
	if m == nil || m.Sender == nil || m.Sender.ID != d.adminID {
		return nil
	}
	qLen, qUsers := d.videoWorker.QueueStats()
	uptime := time.Since(d.startedAt).Truncate(time.Second)
	version := VersionString()
	return c.Reply(fmt.Sprintf(
		"ok\nversion: %s\nuptime: %s\nqueue: %d jobs from %d users\nmaintenance: %v\ncodec: %s",
		version, uptime, qLen, qUsers, d.videoWorker.Maintenance(), d.codec,
	))
}

func findUpdateScript() string {
	candidates := []string{
		os.Getenv("DISTORTIONER_UPDATE_SCRIPT"),
		"update.sh",
		"/src/update.sh",
		"/repo/update.sh",
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "update.sh"))
	}
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

func (d DistorterBot) handleUpdate(c tb.Context) error {
	m := c.Message()
	if m == nil || m.Sender == nil || m.Sender.ID != d.adminID {
		return nil
	}
	branch := strings.TrimSpace(m.Payload)
	if branch == "" {
		branch = os.Getenv("DISTORTIONER_UPDATE_BRANCH")
	}
	if branch == "" {
		branch = "master"
	}
	if !safeBranchRe.MatchString(branch) {
		return c.Reply("Invalid branch name.")
	}

	script := findUpdateScript()
	if script == "" {
		return c.Reply("update.sh not found inside the container. Recreate with the repo mounted, e.g. -v /path/to/Distortioner:/path/to/Distortioner -w /path/to/Distortioner, and set DISTORTIONER_UPDATE_SCRIPT to that update.sh path. Or run ./update.sh on the host once.")
	}

	logPath := os.Getenv("DISTORTIONER_UPDATE_LOG")
	if logPath == "" {
		logPath = filepath.Join("data", "update.log")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		d.logger.Error(err)
		return c.Reply("Could not create update log directory.")
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		d.logger.Error(err)
		return c.Reply("Could not open update log.")
	}

	cmd := exec.Command("bash", script, branch)
	cmd.Dir = filepath.Dir(script)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(),
		"UPDATE_NOTIFY_CHAT_ID="+strconv.FormatInt(m.Chat.ID, 10),
		"UPDATE_BRANCH="+branch,
	)
	if token := os.Getenv("DISTORTIONER_BOT_TOKEN"); token != "" {
		cmd.Env = append(cmd.Env, "DISTORTIONER_BOT_TOKEN="+token)
	}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		d.logger.Error(err)
		return c.Reply("Failed to start update: " + err.Error())
	}
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
	}()

	return c.Reply(fmt.Sprintf("Checking origin/%s for updates…\nScript: %s\nLog: %s", branch, script, logPath))
}

func (d DistorterBot) handleStatRequest(c tb.Context, db *stats.DistortionerDB, period stats.Period) error {
	m := c.Message()
	if m.Sender.ID != d.adminID {
		return nil
	}
	stat, err := db.GetStat(period)
	if err != nil {
		d.logger.Error(err)
		return c.Reply(err.Error())
	}
	header := "Stats for the past %s"
	switch period {
	case stats.Daily:
		header = fmt.Sprintf(header, "24 hours")
	case stats.Weekly:
		header = fmt.Sprintf(header, "week")
	case stats.Monthly:
		header = fmt.Sprintf(header, "month")
	default:
		d.logger.Warnf("stats asked for a weird period", zap.String("period", string(period)))
		return nil
	}
	totalUsers, err := db.TotalUsers()
	if err != nil {
		d.logger.Error(err)
	}
	message := fmt.Sprintf("*%s*\nDistorted %d messages in %d distinct chats, %d of which were group chats\n"+
		"Active users: %d, new: %d, known in total: %d\n",
		header, stat.Interactions, stat.Chats, stat.Groups, stat.Users, stat.NewUsers, totalUsers)
	details := fmt.Sprintf(`
*Breakdown by type*
_Stickers_: %d
_GIFs_: %d
_Videos_: %d
_Video notes_: %d
_Voice messages_: %d
_Photos_: %d
_Text messages_: %d
`,
		stat.Sticker, stat.Animation, stat.Video, stat.VideoNote, stat.Voice, stat.Photo, stat.Text)
	return c.Reply(message+details, tb.ModeMarkdown)
}

func (d DistorterBot) handleQueueStats(c tb.Context) error {
	if c.Message().Sender.ID != d.adminID {
		return nil
	}
	length, users := d.videoWorker.QueueStats()
	return c.Reply(fmt.Sprintf("Currently in queue: %d requests from %d users", length, users))
}

func (d DistorterBot) handleMaintenance(c tb.Context) error {
	if c.Message().Sender.ID != d.adminID {
		return nil
	}
	currentMode := d.videoWorker.ToggleMaintenance()
	return c.Reply(fmt.Sprintf("Maintenance on: %v", currentMode))
}

// recordsStat reports whether a message counts as a distortion request: media and plain text
// always, commands only when they are /distort (SaveStat then records the replied-to media).
func recordsStat(m *tb.Message) (record, isCommand bool) {
	if !tools.IsBotCommand(m) {
		return true, false
	}
	name, _ := splitFirstField(m.Text)
	name, _, _ = strings.Cut(name, "@")
	return name == "/distort", true
}

func main() {
	lg, err := zap.NewProduction()
	if err != nil {
		log.Fatal(err)
	}
	defer lg.Sync() // flushes buffer, if any
	logger := lg.Sugar()
	distorters.SetLogger(logger)
	if dir, err := tools.InitTempDir(); err != nil {
		logger.Fatalw("temp dir", "dir", dir, "err", err)
	}
	db := stats.InitDB(logger)
	defer db.Close()

	adminID, err := strconv.ParseInt(os.Getenv("DISTORTIONER_ADMIN_ID"), 10, 64)
	if err != nil {
		adminID = -1
		logger.Fatal("DISTORTIONER_ADMIN_ID variable is not set")
	}

	b, err := tb.NewBot(tb.Settings{
		Token: os.Getenv("DISTORTIONER_BOT_TOKEN"),
	})
	if err != nil {
		logger.Fatal(err)
	}
	codec := os.Getenv("DISTORTIONER_CODEC")
	if codec == "" {
		codec = "libx264"
	}
	priorityChatsStr := strings.Split(os.Getenv("DISTORTIONER_PRIORITY_CHATS"), ",")
	priorityChats := make([]int64, len(priorityChatsStr))
	for i, s := range priorityChatsStr {
		if s == "" {
			continue
		}
		priorityChats[i], err = strconv.ParseInt(s, 10, 64)
		if err != nil {
			logger.Fatal(err)
		}
	}

	d := DistorterBot{
		adminID:     adminID,
		rl:          tools.NewRateLimiter(),
		logger:      logger,
		graceWg:     &sync.WaitGroup{},
		videoWorker: tools.NewVideoWorker(3, priorityChats),
		codec:       codec,
		db:          db,
		startedAt:   time.Now(),

		bannedNotice:  tools.NewThrottle(time.Hour),
		unbanRequests: tools.NewThrottle(24 * time.Hour),
		chatRights:    newChatRightsCache(chatRightsTTL),
	}
	b.Poller = tb.NewMiddlewarePoller(&tb.LongPoller{Timeout: 10 * time.Second}, func(update *tb.Update) bool {
		if update.Message == nil {
			return true // allow callbacks / non-message updates
		}
		m := update.Message
		text := update.Message.Text
		if m.FromGroup() && !tools.IsCommandForBot(m, b.Me.Username) {
			return false
		}
		// throw away old messages
		if time.Now().Sub(m.Time()) > 2*time.Hour {
			return false
		}
		if m.Sender != nil {
			go db.TouchUser(m.Sender)
		}
		if m.Sender != nil && m.Sender.ID != adminID {
			if reason, banned := db.BanReason(m.Sender.ID); banned && !strings.HasPrefix(text, "/unbanrequest") {
				// Throttled so a banned user spamming the bot can't burn our Telegram send quota.
				if d.bannedNotice.Allow(m.Sender.ID, time.Now()) {
					b.Reply(m, bannedNoticeText(reason))
				}
				return false
			}
		}
		if m.FromGroup() {
			permissions, err := d.chatRights.get(b, m.Chat.ID, time.Now())
			if err != nil {
				logger.Error("Failed to get chat", zap.Int64("chat_id", m.Chat.ID), zap.Error(err))
				return false
			}
			if permissions != nil {
				if !permissions.CanSendMessages {
					logger.Warn("can't send anything at all", zap.Int64("chat_id", m.Chat.ID))
					return false
				} else if (!permissions.CanSendMedia && tools.IsMedia(m.ReplyTo)) || (!permissions.CanSendOther && tools.IsNonMediaMedia(m.ReplyTo)) {
					b.Reply(m, NotEnoughRights)
					return false
				}
			}
		}
		if record, isCommand := recordsStat(m); record {
			db.SaveStat(m, isCommand)
		}
		return true
	})

	if err != nil {
		logger.Fatal(err)
		return
	}

	b.Use(middleware.Recover())
	b.Handle("/start", d.handleStart)

	b.Handle("/intensity", d.handleIntensity)
	b.Handle("/sound", d.handleSound)
	b.Handle("/ramp", d.handleRamp)
	b.Handle("/status", d.handleStatus)
	b.Handle("/update", d.handleUpdate)
	b.Handle("/ban", d.handleBanCommand)
	b.Handle("/unban", d.handleUnbanCommand)
	b.Handle("/bannedusers", d.handleBannedUsers)
	b.Handle("/unbanrequest", d.handleUnbanRequest)
	b.Handle("/user", d.handleUserCommand)
	b.Handle("/topusers", d.handleTopUsers)

	b.Handle(&tb.InlineButton{Unique: "ban"}, d.handleBanCallback)
	b.Handle(&tb.InlineButton{Unique: "unban"}, d.handleUnbanCallback)
	b.Handle(&tb.InlineButton{Unique: "userinfo"}, d.handleUserInfoCallback)

	b.Handle("/daily", func(c tb.Context) error {
		return d.handleStatRequest(c, db, stats.Daily)
	})

	b.Handle("/weekly", func(c tb.Context) error {
		return d.handleStatRequest(c, db, stats.Weekly)
	})

	b.Handle("/monthly", func(c tb.Context) error {
		return d.handleStatRequest(c, db, stats.Monthly)
	})

	b.Handle("/queue", d.handleQueueStats)

	b.Handle("/maintenance", d.handleMaintenance)

	b.Handle("/distort", d.ApplyShutdownMiddleware(d.handleReplyDistortion))
	b.Handle(tb.OnAnimation, d.ApplyShutdownMiddleware(d.withSettings(d.handleAnimationDistortion)))
	b.Handle(tb.OnSticker, d.ApplyShutdownMiddleware(d.withSettings(d.handleStickerDistortion)))
	b.Handle(tb.OnPhoto, d.ApplyShutdownMiddleware(d.withSettings(d.handlePhotoDistortion)))
	b.Handle(tb.OnVoice, d.ApplyShutdownMiddleware(d.withSettings(d.handleVoiceDistortion)))
	b.Handle(tb.OnVideo, d.ApplyShutdownMiddleware(d.withSettings(d.handleVideoDistortion)))
	b.Handle(tb.OnVideoNote, d.ApplyShutdownMiddleware(d.withSettings(d.handleVideoNoteDistortion)))
	b.Handle(tb.OnText, d.ApplyShutdownMiddleware(d.withSettings(d.handleTextDistortion)))

	go func() {
		signChan := make(chan os.Signal, 1)
		signal.Notify(signChan, os.Interrupt, syscall.SIGTERM)
		sig := <-signChan

		logger.Info("shutdown: ", zap.String("signal", sig.String()))
		d.videoWorker.Shutdown()
		d.graceWg.Wait()
		b.Stop()
	}()

	b.Start()
}
