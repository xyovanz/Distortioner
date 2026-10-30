package distorters

import (
	"fmt"
	"math"
	"strings"
)

const (
	SoundModeDistort   = "distort"
	SoundModeBassBoost = "bassboost"
)

// ParseSoundMode maps user input (including aliases) to a known mode.
func ParseSoundMode(mode string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case SoundModeBassBoost, "bass", "bass-boost", "bass_boost":
		return SoundModeBassBoost, true
	case SoundModeDistort, "":
		return SoundModeDistort, true
	default:
		return "", false
	}
}

// NormalizeSoundMode returns a known mode, or SoundModeDistort for empty/unknown values.
func NormalizeSoundMode(mode string) string {
	if m, ok := ParseSoundMode(mode); ok {
		return m
	}
	return SoundModeDistort
}

func soundFilterParts(mode string, intensity int) []string {
	i := clampIntensity(intensity)
	switch NormalizeSoundMode(mode) {
	case SoundModeBassBoost:
		return bassBoostFilterParts(i)
	default:
		return distortFilterParts(i)
	}
}

func distortFilterParts(i int) []string {
	depth, freq := VibratoParams(i)
	parts := []string{fmt.Sprintf("vibrato=f=%.3f:d=%.3f", freq, depth)}

	// Above 50, depth is already maxed — add tremolo / chorus / slight pitch for extra punch.
	if i > 50 {
		t := float64(i-50) / 50.0 // 0..1
		parts = append(parts, fmt.Sprintf("tremolo=f=%.2f:d=%.3f", 3.0+5.0*t, 0.25+0.55*t))
		parts = append(parts, fmt.Sprintf("chorus=0.6:0.9:%.0f:0.4:0.25:%.2f", 40.0+40.0*t, 1.5+2.5*t))
		if i >= 75 {
			pitch := 1.0 + 0.06*t // up to ~6% rate shift
			parts = append(parts, fmt.Sprintf("asetrate=48000*%.4f,aresample=48000", pitch))
		}
	}
	return parts
}

func bassBoostFilterParts(i int) []string {
	// ffmpeg bass gain is capped at ±20; intensity maps into that range and stays loud.
	gain := 8.0 + 12.0*float64(i)/100.0 // ~8.1 … 20
	if gain > 20 {
		gain = 20
	}
	return []string{
		fmt.Sprintf("bass=g=%.1f:f=110:w=0.7", gain),
		fmt.Sprintf("equalizer=f=60:width_type=h:width=50:g=%.1f", gain*0.6),
		"alimiter=limit=0.95:level=disabled",
	}
}

// VibratoParams returns ffmpeg vibrato depth [0,1] and frequency for intensity 1–100.
func VibratoParams(intensity int) (depth, freq float64) {
	i := clampIntensity(intensity)
	depth = math.Min(1.0, 0.1+0.018*float64(i))
	freq = 2.0 + 0.08*float64(i)
	return depth, freq
}

// SoundFilterString is the ffmpeg -af filter graph for tests / debugging.
func SoundFilterString(mode string, intensity int) string {
	return strings.Join(soundFilterParts(mode, intensity), ",")
}

func DistortSound(filename, output string, intensity int, mode string) error {
	release := acquireCPU()
	defer release()
	return runFfmpeg(
		"-i", filename,
		"-vn",
		"-c:a", "libopus",
		"-af", SoundFilterString(mode, intensity),
		output)
}
