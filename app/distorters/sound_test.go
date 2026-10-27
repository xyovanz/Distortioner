package distorters

import (
	"strings"
	"testing"
)

func TestNormalizeSoundMode(t *testing.T) {
	if got := NormalizeSoundMode(""); got != SoundModeDistort {
		t.Fatalf("empty: got %q", got)
	}
	if got := NormalizeSoundMode("bassboost"); got != SoundModeBassBoost {
		t.Fatalf("bassboost: got %q", got)
	}
	if got := NormalizeSoundMode("BASS"); got != SoundModeBassBoost {
		t.Fatalf("bass alias: got %q", got)
	}
	if got := NormalizeSoundMode("nope"); got != SoundModeDistort {
		t.Fatalf("unknown falls back: got %q", got)
	}
}

func TestSoundFilterStringDistort(t *testing.T) {
	af := SoundFilterString(SoundModeDistort, 50)
	if !strings.Contains(af, "vibrato=") {
		t.Fatalf("distort should use vibrato: %s", af)
	}
	if strings.Contains(af, "bass=") {
		t.Fatalf("distort should not use bass: %s", af)
	}
}

func TestSoundFilterStringBassBoost(t *testing.T) {
	af := SoundFilterString(SoundModeBassBoost, 100)
	if !strings.Contains(af, "bass=g=20.0") && !strings.Contains(af, "bass=g=20") {
		t.Fatalf("bassboost at 100 should max bass gain: %s", af)
	}
	if strings.Contains(af, "vibrato=") {
		t.Fatalf("bassboost should not use vibrato: %s", af)
	}
	low := SoundFilterString(SoundModeBassBoost, 1)
	if !strings.Contains(low, "bass=g=") {
		t.Fatalf("bassboost at 1 should still use bass: %s", low)
	}
}
