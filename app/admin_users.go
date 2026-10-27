package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	tb "gopkg.in/telebot.v3"

	"github.com/graynk/distortioner/stats"
)

const (
	topUsersLimit    = 15
	bannedListLimit  = 50
	banHistoryLimit  = 5
	statusSeparator  = "\n\n➖ "
	unbanRequestHelp = "Use /unbanrequest <message> to ask the admin to lift the ban."
)

// Preset reasons offered as inline buttons on admin alerts and user cards.
var banButtonReasons = []string{"Spam", "NSFW", "Other"}

// idLineRe finds the "ID: 123" line in the bot's own alerts and user cards, so the admin can
// reply to them with /ban, /unban or /user.
var idLineRe = regexp.MustCompile(`(?m)^ID: (-?\d+)`)

func (d DistorterBot) isAdmin(c tb.Context) bool {
	s := c.Sender()
	return s != nil && s.ID == d.adminID
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func splitFirstField(s string) (first, rest string) {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, func(r rune) bool { return r == ' ' || r == '\n' || r == '\t' })
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i+1:])
}

// replyTargetID extracts the user an admin reply points at: the original author of a forward,
// the sender of a plain message, or the ID line of one of the bot's own alerts/cards.
func replyTargetID(reply *tb.Message) (int64, bool) {
	if reply == nil {
		return 0, false
	}
	if reply.OriginalSender != nil && !reply.OriginalSender.IsBot {
		return reply.OriginalSender.ID, true
	}
	if reply.Sender != nil && !reply.Sender.IsBot {
		return reply.Sender.ID, true
	}
	text := reply.Text
	if text == "" {
		text = reply.Caption
	}
	if match := idLineRe.FindStringSubmatch(text); match != nil {
		id, err := strconv.ParseInt(match[1], 10, 64)
		return id, err == nil && id != 0
	}
	return 0, false
}

// resolveTarget parses "<id|@username> [rest]" or, when replying, takes the target from the
// replied-to message and treats the whole payload as rest.
func (d DistorterBot) resolveTarget(m *tb.Message) (id int64, rest string, errMsg string) {
	payload := strings.TrimSpace(m.Payload)
	if id, ok := replyTargetID(m.ReplyTo); ok && id != d.adminID {
		return id, payload, ""
	}
	first, rest := splitFirstField(payload)
	if first == "" {
		return 0, "", "missing"
	}
	if id, err := strconv.ParseInt(first, 10, 64); err == nil && id != 0 {
		return id, rest, ""
	}
	if u, ok := d.db.FindUserByUsername(first); ok {
		return u.ID, rest, ""
	}
	return 0, "", fmt.Sprintf("User %s not found. Use a numeric ID, or a @username the bot has seen.", first)
}

func userAdminMarkup(id int64, banned bool) *tb.ReplyMarkup {
	markup := &tb.ReplyMarkup{}
	idStr := strconv.FormatInt(id, 10)
	info := markup.Data("👤 Info", "userinfo", idStr)
	if banned {
		markup.Inline(markup.Row(markup.Data("✅ Unban", "unban", idStr), info))
		return markup
	}
	var row []tb.Btn
	for _, reason := range banButtonReasons {
		row = append(row, markup.Data("🚫 "+reason, "ban", idStr, reason))
	}
	markup.Inline(markup.Row(row...), markup.Row(info))
	return markup
}

func describeAdmin(adminID int64) string {
	if adminID == 0 {
		return "admin"
	}
	return strconv.FormatInt(adminID, 10)
}

func formatUserStat(s stats.UserStat) string {
	return fmt.Sprintf("stickers %d · GIFs %d · videos %d · video notes %d · voice %d · photos %d · text %d",
		s.Sticker, s.Animation, s.Video, s.VideoNote, s.Voice, s.Photo, s.Text)
}

func (d DistorterBot) userCard(id int64) (string, *tb.ReplyMarkup) {
	var b strings.Builder
	fmt.Fprintf(&b, "ID: %d\n", id)
	if u, ok := d.db.GetUser(id); ok {
		fmt.Fprintf(&b, "Name: %s\nFirst seen: %s\nLast seen: %s\n", u.DisplayName(), formatTime(u.FirstSeen), formatTime(u.LastSeen))
	} else {
		b.WriteString("Not seen by the bot yet\n")
	}

	counts := make([]int, 0, 4)
	var month stats.UserStat
	for _, p := range []stats.Period{stats.Daily, stats.Weekly, stats.Monthly, ""} {
		s, err := d.db.GetUserStat(id, p)
		if err != nil {
			d.logger.Error(err)
		}
		if p == stats.Monthly {
			month = s
		}
		counts = append(counts, s.Total)
	}
	fmt.Fprintf(&b, "Requests: 24h %d · 7d %d · 30d %d · all %d\n", counts[0], counts[1], counts[2], counts[3])
	if month.Total > 0 {
		fmt.Fprintf(&b, "Last 30 days: %s\n", formatUserStat(month))
	}

	ramp := "off"
	if from, to, ok := d.db.GetUserRamp(id); ok {
		ramp = fmt.Sprintf("%d→%d", from, to)
	}
	fmt.Fprintf(&b, "Settings: intensity %d · sound %s · ramp %s\n", d.db.GetUserIntensity(id), d.db.GetUserSoundMode(id), ramp)

	ban, banned := d.db.GetBan(id)
	if banned {
		reason := ban.Reason
		if reason == "" {
			reason = "(no reason)"
		}
		fmt.Fprintf(&b, "Status: 🚫 banned since %s by %s — %s\n", formatTime(ban.BannedAt), describeAdmin(ban.BannedBy), reason)
	} else {
		b.WriteString("Status: ✅ not banned\n")
	}

	history, err := d.db.BanHistory(id, banHistoryLimit)
	if err != nil {
		d.logger.Error(err)
	}
	if len(history) > 0 {
		b.WriteString("History:\n")
		for _, e := range history {
			line := fmt.Sprintf("• %s %s", formatTime(e.At), e.Action)
			if e.Reason != "" {
				line += " — " + e.Reason
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String(), userAdminMarkup(id, banned)
}

func (d DistorterBot) handleUserCommand(c tb.Context) error {
	m := c.Message()
	if m == nil || !d.isAdmin(c) {
		return nil
	}
	id, _, errMsg := d.resolveTarget(m)
	if errMsg == "missing" {
		return c.Reply("Usage: /user <id|@username>, or reply to a message with /user")
	}
	if errMsg != "" {
		return c.Reply(errMsg)
	}
	text, markup := d.userCard(id)
	return c.Reply(text, markup)
}

func (d DistorterBot) handleTopUsers(c tb.Context) error {
	m := c.Message()
	if m == nil || !d.isAdmin(c) {
		return nil
	}
	period, label := stats.Weekly, "7 days"
	switch strings.ToLower(strings.TrimSpace(m.Payload)) {
	case "day", "daily", "24h":
		period, label = stats.Daily, "24 hours"
	case "", "week", "weekly", "7d":
	case "month", "monthly", "30d":
		period, label = stats.Monthly, "month"
	default:
		return c.Reply("Usage: /topusers [day|week|month]")
	}
	top, err := d.db.TopUsers(period, topUsersLimit)
	if err != nil {
		d.logger.Error(err)
		return c.Reply("Failed to load stats.")
	}
	if len(top) == 0 {
		return c.Reply("No user activity recorded for the past " + label + ".")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Top users for the past %s:\n", label)
	for i, uc := range top {
		mark := ""
		if d.db.IsBanned(uc.User.ID) {
			mark = " 🚫"
		}
		fmt.Fprintf(&b, "%d. %d — %s — %d%s\n", i+1, uc.User.ID, uc.User.DisplayName(), uc.Count, mark)
	}
	b.WriteString("\n/user <id> for details")
	return c.Reply(b.String())
}

func (d DistorterBot) banTarget(id int64, reason string) (string, error) {
	if id == d.adminID {
		return "Can't ban yourself.", nil
	}
	_, wasBanned := d.db.BanReason(id)
	newly, err := d.db.BanUser(id, d.adminID, reason)
	if err != nil {
		return "", err
	}
	if !newly {
		if wasBanned && reason != "" {
			return fmt.Sprintf("User %d is already banned; reason updated to: %s", id, reason), nil
		}
		return fmt.Sprintf("User %d is already banned.", id), nil
	}
	d.videoWorker.BanUser(id)
	d.bannedNotice.Reset(id)
	if reason == "" {
		return fmt.Sprintf("User %d has been banned.", id), nil
	}
	return fmt.Sprintf("User %d has been banned: %s", id, reason), nil
}

func (d DistorterBot) unbanTarget(c tb.Context, id int64, reason string) (string, error) {
	ok, err := d.db.UnbanUser(id, d.adminID, reason)
	if err != nil {
		return "", err
	}
	if !ok {
		return fmt.Sprintf("User %d was not banned.", id), nil
	}
	d.unbanRequests.Reset(id)
	if _, err := c.Bot().Send(&tb.User{ID: id}, "You have been unbanned. You can use the bot again."); err != nil {
		d.logger.Infow("unban notice not delivered", "user_id", id, "err", err)
	}
	return fmt.Sprintf("User %d has been unbanned.", id), nil
}

func (d DistorterBot) handleBanCommand(c tb.Context) error {
	m := c.Message()
	if m == nil || !d.isAdmin(c) {
		return nil
	}
	id, reason, errMsg := d.resolveTarget(m)
	if errMsg == "missing" {
		return c.Reply("Usage: /ban <id|@username> [reason], or reply to a message with /ban [reason]")
	}
	if errMsg != "" {
		return c.Reply(errMsg)
	}
	text, err := d.banTarget(id, reason)
	if err != nil {
		d.logger.Error(err)
		return c.Reply("Failed to ban.")
	}
	return c.Reply(text)
}

func (d DistorterBot) handleUnbanCommand(c tb.Context) error {
	m := c.Message()
	if m == nil || !d.isAdmin(c) {
		return nil
	}
	id, reason, errMsg := d.resolveTarget(m)
	if errMsg == "missing" {
		return c.Reply("Usage: /unban <id|@username> [reason], or reply to a message with /unban [reason]")
	}
	if errMsg != "" {
		return c.Reply(errMsg)
	}
	text, err := d.unbanTarget(c, id, reason)
	if err != nil {
		d.logger.Error(err)
		return c.Reply("Failed to unban.")
	}
	return c.Reply(text)
}

func (d DistorterBot) handleBannedUsers(c tb.Context) error {
	m := c.Message()
	if m == nil || !d.isAdmin(c) {
		return nil
	}
	bans, err := d.db.BannedUsers()
	if err != nil {
		d.logger.Error(err)
		return c.Reply("Failed to list bans.")
	}
	if len(bans) == 0 {
		return c.Reply("No banned users.")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Banned users (%d):\n", len(bans))
	for i, ban := range bans {
		if i == bannedListLimit {
			fmt.Fprintf(&b, "…and %d more\n", len(bans)-bannedListLimit)
			break
		}
		reason := ban.Reason
		if reason == "" {
			reason = "(no reason)"
		}
		fmt.Fprintf(&b, "%d — %s — %s — %s\n", ban.UserID, ban.User.DisplayName(), ban.BannedAt.UTC().Format("2006-01-02"), reason)
	}
	return c.Reply(b.String())
}

// bannedNoticeText is what a banned user sees (at most once per throttle interval).
func bannedNoticeText(reason string) string {
	text := "You are banned from using this bot."
	if reason != "" {
		text += "\nReason: " + reason
	}
	return text + "\n\n" + unbanRequestHelp
}

func (d DistorterBot) handleUnbanRequest(c tb.Context) error {
	m := c.Message()
	if m == nil || m.Sender == nil {
		return nil
	}
	ban, banned := d.db.GetBan(m.Sender.ID)
	if !banned {
		return c.Reply("You are not banned.")
	}
	if !d.unbanRequests.Allow(m.Sender.ID, time.Now()) {
		return c.Reply("Your unban request was already sent. You can send another one in 24 hours.")
	}
	message := strings.TrimSpace(m.Payload)
	if err := d.db.LogUnbanRequest(m.Sender.ID, message); err != nil {
		d.logger.Error(err)
	}
	user := stats.User{ID: m.Sender.ID, Username: m.Sender.Username, FirstName: m.Sender.FirstName, LastName: m.Sender.LastName}
	reason := ban.Reason
	if reason == "" {
		reason = "(no reason)"
	}
	if message == "" {
		message = "(empty)"
	}
	text := fmt.Sprintf("Unban request\nID: %d\nName: %s\nBanned: %s — %s\nMessage: %s",
		m.Sender.ID, user.DisplayName(), formatTime(ban.BannedAt), reason, message)
	if _, err := c.Bot().Send(&tb.User{ID: d.adminID}, text, userAdminMarkup(m.Sender.ID, true)); err != nil {
		d.logger.Error(err)
		d.unbanRequests.Reset(m.Sender.ID)
		return c.Reply("Could not notify admin. Try again later.")
	}
	return c.Reply("Unban request sent to the admin.")
}

// callbackTarget validates an admin callback and returns its args, answering the callback on error.
func (d DistorterBot) callbackTarget(c tb.Context) (int64, []string, bool) {
	if !d.isAdmin(c) {
		_ = c.Respond(&tb.CallbackResponse{Text: "Unauthorized", ShowAlert: true})
		return 0, nil, false
	}
	args := c.Args()
	if len(args) < 1 {
		_ = c.Respond(&tb.CallbackResponse{Text: "Invalid user ID", ShowAlert: true})
		return 0, nil, false
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id == 0 {
		_ = c.Respond(&tb.CallbackResponse{Text: "Invalid user ID", ShowAlert: true})
		return 0, nil, false
	}
	return id, args[1:], true
}

// markCallbackMessage replaces the status line under an alert/card and swaps its buttons.
func (d DistorterBot) markCallbackMessage(c tb.Context, id int64, status string, banned bool) {
	cb := c.Callback()
	if cb == nil || cb.Message == nil || cb.Message.Text == "" {
		return
	}
	base, _, _ := strings.Cut(cb.Message.Text, statusSeparator)
	if _, err := c.Bot().Edit(cb.Message, base+statusSeparator+status, userAdminMarkup(id, banned)); err != nil {
		d.logger.Infow("callback message edit failed", "err", err)
	}
}

func (d DistorterBot) handleBanCallback(c tb.Context) error {
	id, rest, ok := d.callbackTarget(c)
	if !ok {
		return nil
	}
	reason := strings.Join(rest, " ")
	text, err := d.banTarget(id, reason)
	if err != nil {
		d.logger.Error(err)
		return c.Respond(&tb.CallbackResponse{Text: "Error", ShowAlert: true})
	}
	if d.db.IsBanned(id) {
		status := "🚫 Banned"
		if reason != "" {
			status += ": " + reason
		}
		d.markCallbackMessage(c, id, status, true)
	}
	return c.Respond(&tb.CallbackResponse{Text: text, ShowAlert: true})
}

func (d DistorterBot) handleUnbanCallback(c tb.Context) error {
	id, _, ok := d.callbackTarget(c)
	if !ok {
		return nil
	}
	text, err := d.unbanTarget(c, id, "")
	if err != nil {
		d.logger.Error(err)
		return c.Respond(&tb.CallbackResponse{Text: "Error", ShowAlert: true})
	}
	d.markCallbackMessage(c, id, "✅ Unbanned", false)
	return c.Respond(&tb.CallbackResponse{Text: text, ShowAlert: true})
}

func (d DistorterBot) handleUserInfoCallback(c tb.Context) error {
	id, _, ok := d.callbackTarget(c)
	if !ok {
		return nil
	}
	text, markup := d.userCard(id)
	if _, err := c.Bot().Send(&tb.User{ID: d.adminID}, text, markup); err != nil {
		d.logger.Error(err)
		return c.Respond(&tb.CallbackResponse{Text: "Error", ShowAlert: true})
	}
	return c.Respond()
}
