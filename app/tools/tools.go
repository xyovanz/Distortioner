package tools

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"
	tb "gopkg.in/telebot.v3"
)

const progress = "Processing frames...\n<code>[----------] %d%%</code>"

func GenerateProgressMessage(done, total int) string {
	fraction := float64(done) / float64(total)
	message := fmt.Sprintf(progress, int(fraction*100))
	return strings.Replace(message, "-", "=", int(fraction*10))
}

func IsMedia(m *tb.Message) bool {
	if m == nil {
		return false
	}
	return m.Photo != nil || m.Video != nil || m.VideoNote != nil || m.Voice != nil
}

func IsNonMediaMedia(m *tb.Message) bool {
	if m == nil {
		return false
	}
	return m.Animation != nil || m.Sticker != nil
}

// IsBotCommand reports whether the message is a Telegram bot command (should not be text-distorted).
func IsBotCommand(m *tb.Message) bool {
	if m == nil {
		return false
	}
	if len(m.Entities) > 0 && m.Entities[0].Type == tb.EntityCommand && m.Entities[0].Offset == 0 {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(m.Text), "/")
}

// IsCommandForBot reports whether a group command targets this bot (/cmd or /cmd@BotName).
func IsCommandForBot(m *tb.Message, botUsername string) bool {
	if m == nil || botUsername == "" {
		return false
	}
	if len(m.Entities) == 0 || m.Entities[0].Type != tb.EntityCommand || m.Entities[0].Offset != 0 {
		return false
	}
	ent := m.Entities[0]
	end := ent.Offset + ent.Length
	if ent.Offset < 0 || end > len(m.Text) || end <= ent.Offset {
		return false
	}
	// Bot commands are ASCII; Telegram UTF-16 offsets match bytes here.
	token := m.Text[ent.Offset:end]
	parts := strings.SplitN(token, "@", 2)
	if len(parts) == 1 {
		return true
	}
	return strings.EqualFold(parts[1], botUsername)
}

// KeepFailedTemps is true when DISTORTIONER_KEEP_FAILED=1 (leave files on failure for debugging).
func KeepFailedTemps() bool {
	return os.Getenv("DISTORTIONER_KEEP_FAILED") == "1"
}

// RemoveTemp removes path unless this was a failure and KeepFailedTemps is set.
func RemoveTemp(path string, failed bool) {
	if path == "" {
		return
	}
	if failed && KeepFailedTemps() {
		return
	}
	_ = os.Remove(path)
}

func JustGetTheFile(b *tb.Bot, m *tb.Message) (string, error) {
	filename := uuid.New().String()
	file := m.Media().MediaFile()
	err := b.Download(file, filename)
	if err != nil {
		b.Reply(m, "Failed to download media")
	}

	return filename, err
}

func ExtractPossibleTimeout(err error) (int, error) {
	// format: "telegram: retry after x (429)"
	errorString := err.Error()
	if strings.Contains(errorString, "kicked") {
		return 0, err
	}
	after := "after "
	retryAfterStringEnd := strings.LastIndex(errorString, after)
	if retryAfterStringEnd == -1 {
		return 0, err
	}
	timeoutEnd := strings.LastIndex(errorString, " (")
	if timeoutEnd == -1 {
		timeoutEnd = len(errorString)
	}
	return strconv.Atoi(errorString[retryAfterStringEnd+len(after) : timeoutEnd])
}

func FormatRateLimitResponse(diff int64) string {
	return fmt.Sprintf("Please, not so often. Try again in %d seconds", diff)
}
