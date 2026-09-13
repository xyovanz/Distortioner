package main

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	tb "gopkg.in/telebot.v3"
)

func TestIsReplyMessageMissing(t *testing.T) {
	if !isReplyMessageMissing(tb.ErrNotFoundToReply) {
		t.Fatal("expected telebot.ErrNotFoundToReply to match")
	}
	// Exact Error() string telebot v3 produces — must match or completed media is dropped.
	if !isReplyMessageMissing(errors.New(tb.ErrNotFoundToReply.Error())) {
		t.Fatalf("expected %q to match", tb.ErrNotFoundToReply.Error())
	}
	if !isReplyMessageMissing(fmt.Errorf("telegram: Bad Request: message to be replied not found (400)")) {
		t.Fatal("expected legacy API wording to match")
	}
	// The old buggy needle never appears in telebot v3's Error() output.
	wrong := "telegram: Bad Request: message to be replied not found (400)"
	if tb.ErrNotFoundToReply.Error() == wrong {
		t.Fatal("telebot Error() unexpectedly equals the old needle; test assumption broken")
	}
	if isReplyMessageMissing(tb.ErrBlockedByUser) {
		t.Fatal("blocked-by-user must not be treated as reply-missing")
	}
	if isReplyMessageMissing(nil) {
		t.Fatal("nil must not match")
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
