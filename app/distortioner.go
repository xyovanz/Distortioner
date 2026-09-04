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
	mu          *sync.Mutex
	graceWg     *sync.WaitGroup
	videoWorker *tools.VideoWorker
	codec       string
	db          *stats.DistortionerDB
	startedAt   time.Time
}

func (d DistorterBot) withIntensity(h func(tb.Context, int) error) tb.HandlerFunc {
	return func(c tb.Context) error {
		m := c.Message()
		if m == nil || m.Sender == nil {
			return nil
		}
		return h(c, d.db.GetUserIntensity(m.Sender.ID))
	}
}

func (d DistorterBot) userRamp(userID int64) (from, to int) {
	from, to, ok := d.db.GetUserRamp(userID)
	if !ok {
		return 0, 0
	}
	return from, to
}

func failStatus(err error, progress *tb.Message) string {
	if progress != nil && distorters.IsFailureStatus(progress.Text) {
		return progress.Text
	}
	return distorters.UserFacingError(err)
}

// submitVideoJob gates size/rate-limit, enqueues work, and tells the user their queue position when busy.
func (d DistorterBot) submitVideoJob(c tb.Context, fileSize int64, work func()) error {
	m := c.Message()
	if fileSize > MaxSizeMb {
		return d.SendMessageWithRepeater(c, distorters.TooBig)
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

func (d DistorterBot) handleAnimationDistortion(c tb.Context, intensity int) error {
	m := c.Message()
	b := c.Bot()
	rampFrom, rampTo := d.userRamp(m.Sender.ID)
	return d.submitVideoJob(c, m.Animation.FileSize, func() {
		progressMessage, filename, output, err := d.HandleAnimationCommon(c, intensity, rampFrom, rampTo)
		failed := err != nil
		defer tools.RemoveTemp(filename, failed)
		defer tools.RemoveTemp(output, failed)
		if failed {
			if progressMessage != nil && progressMessage.Text != distorters.TooLong {
				d.DoneMessageWithRepeater(b, progressMessage, failStatus(err, progressMessage))
			}
			d.logger.Error(err)
			return
		}

		distorted := &tb.Animation{File: tb.FromDisk(output), FileName: output}
		if m.Caption != "" {
			distorted.Caption = distorters.DistortText(m.Caption, intensity)
		}
		err = d.SendMessageWithRepeater(c, distorted)
		d.DoneMessageWithRepeater(b, progressMessage, "")
		if err != nil {
			d.logger.Error(err)
		}
	})
}

func (d DistorterBot) handlePhotoDistortion(c tb.Context, intensity int) error {
	m := c.Message()
	filename, err := tools.JustGetTheFile(c.Bot(), m)
	if err != nil {
		d.logger.Error(err)
		return err
	}
	err = distorters.DistortImage(filename, intensity)
	failed := err != nil
	defer tools.RemoveTemp(filename, failed)
	if failed {
		d.SendMessageWithRepeater(c, distorters.FailedDistortImage)
		return err
	}
	distorted := &tb.Photo{File: tb.FromDisk(filename)}
	if m.Caption != "" {
		distorted.Caption = distorters.DistortText(m.Caption, intensity)
	}
	return d.SendMessageWithRepeater(c, distorted)
}

func (d DistorterBot) handleRegularStickerDistortion(c tb.Context, intensity int) error {
	m := c.Message()
	filename, err := tools.JustGetTheFile(c.Bot(), m)
	if err != nil {
		d.logger.Error(err)
		return err
	}
	err = distorters.DistortImage(filename, intensity)
	failed := err != nil
	defer tools.RemoveTemp(filename, failed)
	if failed {
		d.SendMessageWithRepeater(c, distorters.FailedDistortImage)
		return err
	}
	distorted := &tb.Sticker{File: tb.FromDisk(filename)}
	return d.SendMessageWithRepeater(c, distorted)
}

func (d DistorterBot) handleVideoStickerDistortion(c tb.Context, intensity int) error {
	m := c.Message()
	rampFrom, rampTo := d.userRamp(m.Sender.ID)
	return d.submitVideoJob(c, m.Sticker.FileSize, func() {
		filename, output, err := d.HandleVideoSticker(c, intensity, rampFrom, rampTo)
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

func (d DistorterBot) handleStickerDistortion(c tb.Context, intensity int) error {
	m := c.Message()
	var err error
	switch {
	case m.Sticker.Animated:
		err = d.SendMessageWithRepeater(c, AnimatedStickersUnsupported)
	case m.Sticker.Video:
		err = d.handleVideoStickerDistortion(c, intensity)
	default:
		err = d.handleRegularStickerDistortion(c, intensity)
	}
	return err
}

func (d DistorterBot) handleTextDistortion(c tb.Context, intensity int) error {
	m := c.Message()
	if tools.IsBotCommand(m) {
		return nil
	}
	return d.SendMessageWithRepeater(c, distorters.DistortText(c.Text(), intensity))
}

func (d DistorterBot) handleVideoDistortion(c tb.Context, intensity int) error {
	m := c.Message()
	b := c.Bot()
	rampFrom, rampTo := d.userRamp(m.Sender.ID)
	return d.submitVideoJob(c, m.Video.FileSize, func() {
		output, progressMessage, err := d.HandleVideoCommon(c, intensity, rampFrom, rampTo)
		failed := err != nil
		defer tools.RemoveTemp(output, failed)
		if failed {
			if progressMessage != nil && progressMessage.Text != distorters.TooLong {
				d.DoneMessageWithRepeater(b, progressMessage, failStatus(err, progressMessage))
			}
			d.logger.Error(err)
			return
		}

		distorted := &tb.Video{File: tb.FromDisk(output)}
		err = d.SendMessageWithRepeater(c, distorted)
		d.DoneMessageWithRepeater(b, progressMessage, "")
		if err != nil {
			d.logger.Error(err)
		}
	})
}

func (d DistorterBot) handleVideoNoteDistortion(c tb.Context, intensity int) error {
	m := c.Message()
	b := c.Bot()
	rampFrom, rampTo := d.userRamp(m.Sender.ID)
	return d.submitVideoJob(c, m.VideoNote.FileSize, func() {
		output, progressMessage, err := d.HandleVideoCommon(c, intensity, rampFrom, rampTo)
		failed := err != nil
		defer tools.RemoveTemp(output, failed)
		if failed {
			if progressMessage != nil && progressMessage.Text != distorters.TooLong {
				d.logger.Error(err)
				d.DoneMessageWithRepeater(b, progressMessage, failStatus(err, progressMessage))
			}
			return
		}
		distorted := &tb.VideoNote{File: tb.FromDisk(output)}
		err = d.SendMessageWithRepeater(c, distorted)
		d.DoneMessageWithRepeater(b, progressMessage, "")
		if err != nil {
			d.logger.Error(err)
		}
	})
}

func (d DistorterBot) handleVoiceDistortion(c tb.Context, intensity int) error {
	m := c.Message()
	if m.Voice.FileSize > MaxSizeMb {
		return c.Reply(distorters.TooBig)
	}
	filename, err := tools.JustGetTheFile(c.Bot(), m)
	if err != nil {
		d.logger.Error(err)
		return err
	}
	output := filename + ".ogg"
	err = distorters.DistortSound(filename, output, intensity)
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
	intensity := d.db.GetUserIntensity(m.Sender.ID)
	switch {
	case original.Animation != nil:
		return d.handleAnimationDistortion(tweakedContext, intensity)
	case original.Sticker != nil:
		return d.handleStickerDistortion(tweakedContext, intensity)
	case original.Photo != nil:
		return d.handlePhotoDistortion(tweakedContext, intensity)
	case original.Voice != nil:
		return d.handleVoiceDistortion(tweakedContext, intensity)
	case original.Video != nil:
		return d.handleVideoDistortion(tweakedContext, intensity)
	case original.VideoNote != nil:
		return d.handleVideoNoteDistortion(tweakedContext, intensity)
	case original.Text != "":
		return d.handleTextDistortion(tweakedContext, intensity)
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
		"Use /intensity (1–100) for strength, /ramp to toggle progressive frames (or /ramp <from> <to>)."
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
	markup := &tb.ReplyMarkup{}
	btn := markup.Data("🚫 Ban User", "ban", strconv.FormatInt(m.Sender.ID, 10))
	markup.Inline(markup.Row(btn))
	if _, err := c.Bot().Send(&tb.User{ID: d.adminID}, adminText, markup); err != nil {
		d.logger.Errorw("admin start alert failed", "err", err)
	}
	if _, err := c.Bot().Forward(&tb.User{ID: d.adminID}, m); err != nil {
		d.logger.Errorw("forward /start failed", "err", err)
	}
	return nil
}

func (d DistorterBot) handleBanCommand(c tb.Context) error {
	m := c.Message()
	if m == nil || m.Sender == nil || m.Sender.ID != d.adminID {
		return nil
	}
	payload := strings.TrimSpace(m.Payload)
	id, err := strconv.ParseInt(payload, 10, 64)
	if err != nil || id == 0 {
		return c.Reply("Usage: /ban <user_id>")
	}
	if id == d.adminID {
		return c.Reply("Can't ban yourself.")
	}
	ok, err := d.db.BanUser(id)
	if err != nil {
		d.logger.Error(err)
		return c.Reply("Failed to ban.")
	}
	if !ok {
		return c.Reply(fmt.Sprintf("User %d is already banned.", id))
	}
	d.videoWorker.BanUser(id)
	return c.Reply(fmt.Sprintf("User %d has been banned.", id))
}

func (d DistorterBot) handleUnbanCommand(c tb.Context) error {
	m := c.Message()
	if m == nil || m.Sender == nil || m.Sender.ID != d.adminID {
		return nil
	}
	payload := strings.TrimSpace(m.Payload)
	id, err := strconv.ParseInt(payload, 10, 64)
	if err != nil || id == 0 {
		return c.Reply("Usage: /unban <user_id>")
	}
	ok, err := d.db.UnbanUser(id)
	if err != nil {
		d.logger.Error(err)
		return c.Reply("Failed to unban.")
	}
	if !ok {
		return c.Reply(fmt.Sprintf("User %d was not banned.", id))
	}
	return c.Reply(fmt.Sprintf("User %d has been unbanned.", id))
}

func (d DistorterBot) handleBannedUsers(c tb.Context) error {
	m := c.Message()
	if m == nil || m.Sender == nil || m.Sender.ID != d.adminID {
		return nil
	}
	ids, err := d.db.BannedUsers()
	if err != nil {
		d.logger.Error(err)
		return c.Reply("Failed to list bans.")
	}
	if len(ids) == 0 {
		return c.Reply("No banned users.")
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Banned users (%d):\n", len(ids)))
	for _, id := range ids {
		b.WriteString(fmt.Sprintf("%d\n", id))
	}
	return c.Reply(b.String())
}

func (d DistorterBot) handleUnbanRequest(c tb.Context) error {
	m := c.Message()
	if m == nil || m.Sender == nil {
		return nil
	}
	if !d.db.IsBanned(m.Sender.ID) {
		return c.Reply("You are not banned.")
	}
	reason := strings.TrimSpace(m.Payload)
	username := "N/A"
	if m.Sender.Username != "" {
		username = "@" + m.Sender.Username
	}
	msg := fmt.Sprintf("Unban request\nID: %d\nUsername: %s\nReason: %s", m.Sender.ID, username, reason)
	if reason == "" {
		msg = fmt.Sprintf("Unban request\nID: %d\nUsername: %s\n(no reason)", m.Sender.ID, username)
	}
	markup := &tb.ReplyMarkup{}
	btnUnban := markup.Data("✅ Unban", "unban", strconv.FormatInt(m.Sender.ID, 10))
	markup.Inline(markup.Row(btnUnban))
	if _, err := c.Bot().Send(&tb.User{ID: d.adminID}, msg, markup); err != nil {
		d.logger.Error(err)
		return c.Reply("Could not notify admin. Try again later.")
	}
	return c.Reply("Unban request sent to the admin.")
}

func (d DistorterBot) handleBanCallback(c tb.Context) error {
	cb := c.Callback()
	if cb == nil || cb.Sender == nil || cb.Sender.ID != d.adminID {
		_ = c.Respond(&tb.CallbackResponse{Text: "Unauthorized", ShowAlert: true})
		return nil
	}
	args := c.Args()
	if len(args) < 1 {
		_ = c.Respond(&tb.CallbackResponse{Text: "Invalid user ID", ShowAlert: true})
		return nil
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id == 0 {
		_ = c.Respond(&tb.CallbackResponse{Text: "Invalid user ID", ShowAlert: true})
		return nil
	}
	ok, err := d.db.BanUser(id)
	if err != nil {
		d.logger.Error(err)
		_ = c.Respond(&tb.CallbackResponse{Text: "Error", ShowAlert: true})
		return nil
	}
	if !ok {
		_ = c.Respond(&tb.CallbackResponse{Text: fmt.Sprintf("User %d already banned", id), ShowAlert: true})
		return nil
	}
	d.videoWorker.BanUser(id)
	if cb.Message != nil {
		_, _ = c.Bot().Edit(cb.Message, cb.Message.Text+"\n\n✅ User has been banned")
	}
	return c.Respond(&tb.CallbackResponse{Text: fmt.Sprintf("Banned %d", id), ShowAlert: true})
}

func (d DistorterBot) handleUnbanCallback(c tb.Context) error {
	cb := c.Callback()
	if cb == nil || cb.Sender == nil || cb.Sender.ID != d.adminID {
		_ = c.Respond(&tb.CallbackResponse{Text: "Unauthorized", ShowAlert: true})
		return nil
	}
	args := c.Args()
	if len(args) < 1 {
		_ = c.Respond(&tb.CallbackResponse{Text: "Invalid user ID", ShowAlert: true})
		return nil
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id == 0 {
		_ = c.Respond(&tb.CallbackResponse{Text: "Invalid user ID", ShowAlert: true})
		return nil
	}
	ok, err := d.db.UnbanUser(id)
	if err != nil {
		d.logger.Error(err)
		_ = c.Respond(&tb.CallbackResponse{Text: "Error", ShowAlert: true})
		return nil
	}
	if !ok {
		_ = c.Respond(&tb.CallbackResponse{Text: fmt.Sprintf("User %d was not banned", id), ShowAlert: true})
		return nil
	}
	if cb.Message != nil {
		_, _ = c.Bot().Edit(cb.Message, cb.Message.Text+"\n\n✅ User has been unbanned")
	}
	return c.Respond(&tb.CallbackResponse{Text: fmt.Sprintf("Unbanned %d", id), ShowAlert: true})
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
	message := fmt.Sprintf("*%s*\nDistorted %d messages in %d distinct chats, %d of which were group chats\n",
		header, stat.Interactions, stat.Chats, stat.Groups)
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

func skipStatCommand(text string) bool {
	switch {
	case text == "/daily", text == "/weekly", text == "/monthly", text == "/queue", text == "/maintenance", text == "/update", text == "/status",
		text == "/ban", text == "/unban", text == "/bannedusers", text == "/unbanrequest", text == "/start":
		return true
	case strings.HasPrefix(text, "/intensity"), strings.HasPrefix(text, "/ramp"), strings.HasPrefix(text, "/update@"),
		strings.HasPrefix(text, "/ban"), strings.HasPrefix(text, "/unban"), strings.HasPrefix(text, "/bannedusers"),
		strings.HasPrefix(text, "/unbanrequest"), strings.HasPrefix(text, "/start@"):
		return true
	case strings.HasPrefix(text, "/daily@"), strings.HasPrefix(text, "/weekly@"), strings.HasPrefix(text, "/monthly@"),
		strings.HasPrefix(text, "/queue@"), strings.HasPrefix(text, "/maintenance@"), strings.HasPrefix(text, "/status@"):
		return true
	default:
		return false
	}
}

func main() {
	lg, err := zap.NewProduction()
	if err != nil {
		log.Fatal(err)
	}
	defer lg.Sync() // flushes buffer, if any
	logger := lg.Sugar()
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
		mu:          &sync.Mutex{},
		graceWg:     &sync.WaitGroup{},
		videoWorker: tools.NewVideoWorker(3, priorityChats),
		codec:       codec,
		db:          db,
		startedAt:   time.Now(),
	}
	b.Poller = tb.NewMiddlewarePoller(&tb.LongPoller{Timeout: 10 * time.Second}, func(update *tb.Update) bool {
		if update.Message == nil {
			return true // allow callbacks / non-message updates
		}
		m := update.Message
		isCommand := len(m.Entities) > 0 && m.Entities[0].Type == tb.EntityCommand
		text := update.Message.Text
		if m.FromGroup() && !tools.IsCommandForBot(m, b.Me.Username) {
			return false
		}
		// throw away old messages
		if time.Now().Sub(m.Time()) > 2*time.Hour {
			return false
		}
		if m.Sender != nil && m.Sender.ID != adminID && db.IsBanned(m.Sender.ID) {
			if !(strings.HasPrefix(text, "/unbanrequest")) {
				b.Reply(m, "You are banned from using this bot.\n\nUse /unbanrequest to request an unban.")
				return false
			}
		}
		if m.FromGroup() {
			chat, err := b.ChatByID(m.Chat.ID)
			if err != nil {
				logger.Error("Failed to get chat", zap.Int64("chat_id", m.Chat.ID), zap.Error(err))
				return false
			}
			permissions := chat.Permissions
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
		if !skipStatCommand(text) {
			go db.SaveStat(update.Message, isCommand)
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
	b.Handle("/ramp", d.handleRamp)
	b.Handle("/status", d.handleStatus)
	b.Handle("/update", d.handleUpdate)
	b.Handle("/ban", d.handleBanCommand)
	b.Handle("/unban", d.handleUnbanCommand)
	b.Handle("/bannedusers", d.handleBannedUsers)
	b.Handle("/unbanrequest", d.handleUnbanRequest)

	banBtn := &tb.InlineButton{Unique: "ban"}
	unbanBtn := &tb.InlineButton{Unique: "unban"}
	b.Handle(banBtn, d.handleBanCallback)
	b.Handle(unbanBtn, d.handleUnbanCallback)

	b.Handle("/daily", d.ApplyShutdownMiddleware(func(c tb.Context) error {
		return d.handleStatRequest(c, db, stats.Daily)
	}))

	b.Handle("/weekly", d.ApplyShutdownMiddleware(func(c tb.Context) error {
		return d.handleStatRequest(c, db, stats.Weekly)
	}))

	b.Handle("/monthly", d.ApplyShutdownMiddleware(func(c tb.Context) error {
		return d.handleStatRequest(c, db, stats.Monthly)
	}))

	b.Handle("/queue", d.handleQueueStats)

	b.Handle("/maintenance", d.handleMaintenance)

	b.Handle("/distort", d.ApplyShutdownMiddleware(d.handleReplyDistortion))
	b.Handle(tb.OnAnimation, d.ApplyShutdownMiddleware(d.withIntensity(d.handleAnimationDistortion)))
	b.Handle(tb.OnSticker, d.ApplyShutdownMiddleware(d.withIntensity(d.handleStickerDistortion)))
	b.Handle(tb.OnPhoto, d.ApplyShutdownMiddleware(d.withIntensity(d.handlePhotoDistortion)))
	b.Handle(tb.OnVoice, d.ApplyShutdownMiddleware(d.withIntensity(d.handleVoiceDistortion)))
	b.Handle(tb.OnVideo, d.ApplyShutdownMiddleware(d.withIntensity(d.handleVideoDistortion)))
	b.Handle(tb.OnVideoNote, d.ApplyShutdownMiddleware(d.withIntensity(d.handleVideoNoteDistortion)))
	b.Handle(tb.OnText, d.ApplyShutdownMiddleware(d.withIntensity(d.handleTextDistortion)))

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
