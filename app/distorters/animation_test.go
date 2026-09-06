package distorters

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A frame distort failure used to leave workers blocked forever: the defer always
// sent +1 after -1, the consumer returned without draining, and the small doneChan
// buffer filled up. This reproduces that schedule with a tiny buffer.
func TestPoolDistortImages_FailureDoesNotHangWorkers(t *testing.T) {
	dir := t.TempDir()
	const n = 32
	for i := 0; i < n; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%04d.png", i))
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	orig := distortFrame
	t.Cleanup(func() { distortFrame = orig })

	var calls int
	distortFrame = func(path string, intensity int) error {
		calls++
		// Fail mid-pool so many workers still need to send completions afterward.
		if calls == 3 {
			return errors.New("boom")
		}
		time.Sleep(5 * time.Millisecond)
		return nil
	}

	doneChan := make(chan int, 8) // same buffer size as production
	go poolDistortImages(dir, doneChan, 50, 0, 0)

	finished := make(chan error, 1)
	go func() {
		distorted := 0
		for total := <-doneChan; distorted != total; {
			msg := <-doneChan
			if msg == -1 {
				drainFrameSignals(doneChan, distorted+1, total)
				finished <- nil
				return
			}
			distorted += msg
		}
		finished <- errors.New("expected a frame failure")
	}()

	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out: workers likely blocked on doneChan after frame failure")
	}
}
