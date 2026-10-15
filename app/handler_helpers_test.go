package main

import (
	"sync"
	"testing"
	"time"

	tb "gopkg.in/telebot.v3"
)

func TestReplyMediaMessage_UsesRequesterSender(t *testing.T) {
	requester := &tb.User{ID: 42}
	cmd := &tb.Message{Sender: requester, Text: "/distort"}
	// Channel / anonymous posts often omit From → nil Sender on ReplyTo.
	media := &tb.Message{Video: &tb.Video{}, Sender: nil}
	got := replyMediaMessage(cmd, media)
	if got == nil || got.Sender == nil || got.Sender.ID != 42 {
		t.Fatalf("expected requester sender on media copy, got %#v", got)
	}
	if media.Sender != nil {
		t.Fatal("original ReplyTo must not be mutated")
	}
	if got.Video == nil {
		t.Fatal("media payload must be preserved")
	}

	author := &tb.User{ID: 99}
	media.Sender = author
	got = replyMediaMessage(cmd, media)
	if got.Sender.ID != 42 {
		t.Fatalf("must prefer /distort requester over media author, got %d", got.Sender.ID)
	}
}

// Recover middleware swallows panics; Done must still run or graceWg.Wait hangs forever on shutdown.
func TestApplyShutdownMiddleware_DoneOnPanic(t *testing.T) {
	d := DistorterBot{graceWg: &sync.WaitGroup{}}
	h := d.ApplyShutdownMiddleware(func(c tb.Context) error {
		panic("simulated handler panic")
	})

	func() {
		defer func() { _ = recover() }()
		_ = h(nil)
	}()

	done := make(chan struct{})
	go func() {
		d.graceWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("graceWg.Wait blocked after panic; Done was skipped")
	}
}

func TestApplyShutdownMiddleware_DoneOnSuccess(t *testing.T) {
	d := DistorterBot{graceWg: &sync.WaitGroup{}}
	h := d.ApplyShutdownMiddleware(func(c tb.Context) error {
		return nil
	})
	if err := h(nil); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		d.graceWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("graceWg.Wait blocked after success")
	}
}
