package distorters

import (
	"log"
	"os/exec"
	"strconv"
	"syscall"

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
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
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
