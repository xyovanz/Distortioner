package distorters

import (
	"log"
	"math"
	"os/exec"
	"strconv"

	"github.com/pkg/errors"
)

func clampIntensity(i int) int {
	if i < 1 {
		return 1
	}
	if i > 100 {
		return 100
	}
	return i
}

// ProgressiveIntensity ramps distortion linearly from→to across frames (index 0..total-1).
func ProgressiveIntensity(from, to, index, total int) int {
	from = clampIntensity(from)
	to = clampIntensity(to)
	if total <= 1 || from == to {
		return from
	}
	if index < 0 {
		index = 0
	}
	if index >= total {
		index = total - 1
	}
	t := float64(index) / float64(total-1)
	return clampIntensity(from + int(math.Round(t*float64(to-from))))
}

// FrameIntensity uses flat intensity unless a /ramp from→to is enabled (rampFrom >= 1).
func FrameIntensity(flat, rampFrom, rampTo, index, total int) int {
	if rampFrom < 1 {
		return clampIntensity(flat)
	}
	return ProgressiveIntensity(rampFrom, rampTo, index, total)
}

func liquidRescaleFraction(intensity int) (liquid float64, resizeStretch float64) {
	i := clampIntensity(intensity)
	switch {
	case i <= 50:
		// 1 → mild (~95%), 50 → original bot default (50% liquid-rescale)
		liquid = 95.0 - float64(i-1)*(45.0/49.0)
	default:
		// 50 → 50%, 100 → strong (~25%)
		liquid = 50.0 - float64(i-50)*(25.0/50.0)
	}
	resizeStretch = 100.0 / (liquid / 100.0)
	return liquid, resizeStretch
}

func DistortImage(path string, intensity int) error {
	liquid, resizeStretch := liquidRescaleFraction(intensity)
	cmd := exec.Command(
		"magick",
		path,
		"-resize", "512x512>", // A reasonable cutoff, I hope
		"-liquid-rescale", formatPercent(liquid),
		"-resize", formatPercent(resizeStretch),
		path)
	setProcessGroup(cmd)
	err := cmd.Run()
	if err != nil {
		err = errors.WithStack(err)
		log.Println(err)
	}
	return err
}

func formatPercent(x float64) string {
	return strconv.FormatFloat(x, 'f', 2, 64) + "%"
}
