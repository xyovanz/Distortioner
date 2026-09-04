package distorters

import "testing"

func TestProgressiveIntensity(t *testing.T) {
	if got := ProgressiveIntensity(50, 0, 1); got != 50 {
		t.Fatalf("single frame: got %d want 50", got)
	}
	start := ProgressiveIntensity(100, 0, 10)
	mid := ProgressiveIntensity(100, 5, 10)
	end := ProgressiveIntensity(100, 9, 10)
	if start >= mid || mid >= end {
		t.Fatalf("expected ramp start=%d mid=%d end=%d", start, mid, end)
	}
	if end != 100 {
		t.Fatalf("last frame: got %d want 100", end)
	}
	if start != 40 { // 100 * 2/5
		t.Fatalf("first frame: got %d want 40", start)
	}
}
