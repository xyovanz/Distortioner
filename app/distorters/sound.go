package distorters

import (
	"fmt"
)

func DistortSound(filename, output string, intensity int) error {
	i := clampIntensity(intensity)
	// Intensity 50 ≈ original (f=6, d=1). Scale out from there.
	depth := 0.1 + 0.018*float64(i)
	freq := 2.0 + 0.08*float64(i)
	filter := fmt.Sprintf("vibrato=f=%.3f:d=%.3f", freq, depth)
	return runFfmpeg(
		"-i", filename,
		"-vn",
		"-c:a", "libopus",
		"-af", filter,
		output)
}
