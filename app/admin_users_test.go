package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestSplitFirstField(t *testing.T) {
	first, rest := splitFirstField("  123   spam and  more ")
	require.Equal(t, "123", first)
	require.Equal(t, "spam and  more", rest)
	first, rest = splitFirstField("@cat")
	require.Equal(t, "@cat", first)
	require.Empty(t, rest)
	first, _ = splitFirstField("")
	require.Empty(t, first)
}

func TestReplyTargetID(t *testing.T) {
	bot := &tb.User{ID: 1, IsBot: true}
	cases := []struct {
		name  string
		reply *tb.Message
		want  int64
		ok    bool
	}{
		{"nil", nil, 0, false},
		{"forward", &tb.Message{Sender: bot, OriginalSender: &tb.User{ID: 77}}, 77, true},
		{"plain", &tb.Message{Sender: &tb.User{ID: 55}}, 55, true},
		{"alert", &tb.Message{Sender: bot, Text: "АЛЯРМ!\n\nID: 4242\nИмя: x"}, 4242, true},
		{"card", &tb.Message{Sender: bot, Text: "ID: 9\nName: y"}, 9, true},
		{"hidden forward", &tb.Message{Sender: bot, Text: "hello"}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := replyTargetID(tc.reply)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestUserAdminMarkupFitsCallbackLimit(t *testing.T) {
	for _, banned := range []bool{false, true} {
		markup := userAdminMarkup(-1001234567890123, banned)
		for _, row := range markup.InlineKeyboard {
			for _, btn := range row {
				// Telegram rejects callback_data over 64 bytes.
				require.LessOrEqual(t, len("\f"+btn.Unique+"|"+btn.Data), 64, btn.Text)
			}
		}
	}
}

func TestBannedNoticeText(t *testing.T) {
	require.Contains(t, bannedNoticeText("spam"), "Reason: spam")
	require.False(t, strings.Contains(bannedNoticeText(""), "Reason"))
}
