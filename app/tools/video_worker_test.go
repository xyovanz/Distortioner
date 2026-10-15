package tools

import (
	"testing"
	"time"

	"github.com/graynk/distortioner/queue"
)

// Submit used to buffer only 300 messenger signals while Push accepted up to
// MaxQueueLen+1 jobs. With no free workers, the 301st Submit blocked the caller
// (and thus the Telegram update loop) even though the queue still had room.
func TestSubmitDoesNotBlockBeforeQueueLimit(t *testing.T) {
	// Zero workers: messenger never drains — isolates channel capacity.
	vw := NewVideoWorker(0, []int64{1})
	defer vw.Shutdown()

	target := 500 // well above the old 300 buffer, under MaxQueueLen
	done := make(chan error, 1)
	go func() {
		for i := 0; i < target; i++ {
			if _, err := vw.Submit(1, 1, func() {}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Submit failed early: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Submit blocked after filling messenger; capacity must be >= %d (MaxQueueLen=%d)",
			queue.MaxQueueLen+1, queue.MaxQueueLen)
	}

	if got := vw.queue.Len(); got != target {
		t.Fatalf("queue len=%d want %d", got, target)
	}
}

func TestSubmitRejectsAtQueueLimitWithoutHanging(t *testing.T) {
	vw := NewVideoWorker(0, []int64{1})
	defer vw.Shutdown()

	done := make(chan error, 1)
	go func() {
		for {
			_, err := vw.Submit(1, 1, func() {})
			if err != nil {
				done <- err
				return
			}
		}
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected queue-full error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Submit hung instead of rejecting at MaxQueueLen")
	}

	if got := vw.queue.Len(); got != queue.MaxQueueLen+1 {
		t.Fatalf("queue len=%d want %d", got, queue.MaxQueueLen+1)
	}
}
