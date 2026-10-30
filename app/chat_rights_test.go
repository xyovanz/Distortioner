package main

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestChatRightsCache(t *testing.T) {
	c := newChatRightsCache(time.Minute)
	calls := 0
	fail := false
	c.fetch = func(_ *tb.Bot, chatID int64) (*tb.Rights, error) {
		calls++
		if fail {
			return nil, errors.New("boom")
		}
		return &tb.Rights{CanSendMessages: true}, nil
	}
	now := time.Now()
	r, err := c.get(nil, -1, now)
	require.NoError(t, err)
	require.True(t, r.CanSendMessages)
	_, _ = c.get(nil, -1, now.Add(30*time.Second))
	require.Equal(t, 1, calls, "fresh entry served from cache")

	fail = true
	_, err = c.get(nil, -1, now.Add(2*time.Minute))
	require.Error(t, err)
	_, err = c.get(nil, -1, now.Add(2*time.Minute))
	require.Error(t, err, "errors are not cached")
	require.Equal(t, 3, calls)
}

func TestRecordsStat(t *testing.T) {
	cmd := func(text string) *tb.Message {
		return &tb.Message{Text: text, Entities: tb.Entities{{Type: tb.EntityCommand, Offset: 0, Length: len(text)}}}
	}
	cases := []struct {
		m             *tb.Message
		record, isCmd bool
	}{
		{&tb.Message{Text: "hello"}, true, false},
		{&tb.Message{Photo: &tb.Photo{}}, true, false},
		{cmd("/distort"), true, true},
		{cmd("/distort@SomeBot"), true, true},
		{cmd("/intensity"), false, true},
		{cmd("/whatever"), false, true},
	}
	for _, tc := range cases {
		record, isCmd := recordsStat(tc.m)
		require.Equal(t, tc.record, record, tc.m.Text)
		require.Equal(t, tc.isCmd, isCmd, tc.m.Text)
	}
}
