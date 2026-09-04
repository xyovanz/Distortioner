package distorters

import (
	"math"
	"testing"
)

func TestProgressiveIntensity(t *testing.T) {
	if got := ProgressiveIntensity(20, 80, 0, 1); got != 20 {
		t.Fatalf("single frame: got %d want 20", got)
	}
	start := ProgressiveIntensity(20, 80, 0, 10)
	mid := ProgressiveIntensity(20, 80, 5, 10)
	end := ProgressiveIntensity(20, 80, 9, 10)
	if start != 20 {
		t.Fatalf("first frame: got %d want 20", start)
	}
	if end != 80 {
		t.Fatalf("last frame: got %d want 80", end)
	}
	if start >= mid || mid >= end {
		t.Fatalf("expected ramp start=%d mid=%d end=%d", start, mid, end)
	}
	if got := ProgressiveIntensity(50, 50, 3, 10); got != 50 {
		t.Fatalf("flat from==to: got %d want 50", got)
	}
}

func TestFrameIntensity(t *testing.T) {
	if got := FrameIntensity(75, 0, 0, 0, 10); got != 75 {
		t.Fatalf("ramp off: got %d want 75", got)
	}
	if got := FrameIntensity(75, 10, 90, 0, 10); got != 10 {
		t.Fatalf("ramp on first: got %d want 10", got)
	}
	if got := FrameIntensity(75, 10, 90, 9, 10); got != 90 {
		t.Fatalf("ramp on last: got %d want 90", got)
	}
}

func TestLiquidRescaleFraction(t *testing.T) {
	liq1, _ := liquidRescaleFraction(1)
	liq50, _ := liquidRescaleFraction(50)
	liq100, _ := liquidRescaleFraction(100)
	if math.Abs(liq1-95) > 0.01 {
		t.Fatalf("intensity 1: liquid=%v want ~95", liq1)
	}
	if math.Abs(liq50-50) > 0.01 {
		t.Fatalf("intensity 50: liquid=%v want 50", liq50)
	}
	if math.Abs(liq100-25) > 0.01 {
		t.Fatalf("intensity 100: liquid=%v want 25", liq100)
	}
	if !(liq1 > liq50 && liq50 > liq100) {
		t.Fatalf("liquid should decrease with intensity: %v %v %v", liq1, liq50, liq100)
	}
}

func TestVibratoParams(t *testing.T) {
	d50, f50 := VibratoParams(50)
	if math.Abs(d50-1.0) > 0.001 {
		t.Fatalf("depth at 50: got %v want 1", d50)
	}
	d100, f100 := VibratoParams(100)
	if d100 != 1.0 {
		t.Fatalf("depth at 100 should clamp to 1, got %v", d100)
	}
	if f100 <= f50 {
		t.Fatalf("freq should rise with intensity: %v vs %v", f100, f50)
	}
	d1, _ := VibratoParams(1)
	if d1 >= d50 {
		t.Fatalf("depth at 1 should be below 50: %v vs %v", d1, d50)
	}
}

func TestTextStride(t *testing.T) {
	if got := TextStride(50); got != 1 {
		t.Fatalf("stride at 50: got %d want 1", got)
	}
	if got := TextStride(100); got != 1 {
		t.Fatalf("stride at 100: got %d want 1", got)
	}
	if got := TextStride(1); got <= 1 {
		t.Fatalf("stride at 1 should be >1, got %d", got)
	}
	full := DistortText("abcdef", 100)
	if full != "aBcDeF" {
		t.Fatalf("full flip: got %q want aBcDeF", full)
	}
	sparse := DistortText("abcdefghijkl", 1)
	dense := DistortText("abcdefghijkl", 100)
	if sparse == dense {
		t.Fatalf("low intensity should flip fewer chars: %q vs %q", sparse, dense)
	}
}

func TestFormatQueued(t *testing.T) {
	if got := FormatQueued(0); got != Queued {
		t.Fatalf("pos 0: got %q", got)
	}
	if got := FormatQueued(3); got != "Your message has been queued (position 3)" {
		t.Fatalf("pos 3: got %q", got)
	}
}
