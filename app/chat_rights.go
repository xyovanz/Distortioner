package main

import (
	"sync"
	"time"

	tb "gopkg.in/telebot.v3"
)

// chatRightsTTL bounds how stale a group's cached permissions may get. A chat whose rights were
// revoked in the meantime still fails cleanly in SendMessage.
const chatRightsTTL = 10 * time.Minute

const chatRightsMaxEntries = 10_000

type chatRightsEntry struct {
	rights *tb.Rights
	at     time.Time
}

// chatRightsCache avoids a getChat round-trip, made inside the update loop, for every group message.
type chatRightsCache struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[int64]chatRightsEntry
	fetch   func(b *tb.Bot, chatID int64) (*tb.Rights, error)
}

func newChatRightsCache(ttl time.Duration) *chatRightsCache {
	return &chatRightsCache{
		ttl:     ttl,
		entries: make(map[int64]chatRightsEntry),
		fetch: func(b *tb.Bot, chatID int64) (*tb.Rights, error) {
			chat, err := b.ChatByID(chatID)
			if err != nil {
				return nil, err
			}
			return chat.Permissions, nil
		},
	}
}

// get returns the chat's default member permissions (nil when Telegram doesn't report any).
// Errors are not cached.
func (c *chatRightsCache) get(b *tb.Bot, chatID int64, now time.Time) (*tb.Rights, error) {
	c.mu.Lock()
	e, ok := c.entries[chatID]
	c.mu.Unlock()
	if ok && now.Sub(e.at) < c.ttl {
		return e.rights, nil
	}
	rights, err := c.fetch(b, chatID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if len(c.entries) >= chatRightsMaxEntries {
		for id, old := range c.entries {
			if now.Sub(old.at) >= c.ttl {
				delete(c.entries, id)
			}
		}
		if len(c.entries) >= chatRightsMaxEntries {
			c.entries = make(map[int64]chatRightsEntry)
		}
	}
	c.entries[chatID] = chatRightsEntry{rights: rights, at: now}
	c.mu.Unlock()
	return rights, nil
}
