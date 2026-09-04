package distorters

import (
	"fmt"
	"math"
	"strings"
)

func soundFilterParts(intensity int) []string {
	i := clampIntensity(intensity)
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

// VibratoParams returns ffmpeg vibrato depth [0,1] and frequency for intensity 1–100.
func VibratoParams(intensity int) (depth, freq float64) {
	i := clampIntensity(intensity)
	depth = math.Min(1.0, 0.1+0.018*float64(i))
	freq = 2.0 + 0.08*float64(i)
	return depth, freq
}

func DistortSound(filename, output string, intensity int) error {
	return runFfmpeg(
		"-i", filename,
		"-vn",
		"-c:a", "libopus",
		"-af", strings.Join(soundFilterParts(intensity), ","),
		output)
}
