package distorters

import (
	"fmt"
	"math"
)

func DistortSound(filename, output string, intensity int) error {
	i := clampIntensity(intensity)
	// ffmpeg vibrato depth must be in [0, 1]. Intensity 50 ≈ original d=1; higher only bumps frequency.
	depth := math.Min(1.0, 0.1+0.018*float64(i))
	freq := 2.0 + 0.08*float64(i)
	filter := fmt.Sprintf("vibrato=f=%.3f:d=%.3f", freq, depth)
	return runFfmpeg(
		"-i", filename,
		"-vn",
		"-c:a", "libopus",
		"-af", filter,
		output)
}
