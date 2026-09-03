package main

import (
	"sync"
	"testing"
	"time"

	tb "gopkg.in/telebot.v3"
)

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
