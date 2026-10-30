package distorters

import (
	"bytes"
	"os/exec"
	"runtime"
	"strings"

	"github.com/pkg/errors"
	"go.uber.org/zap"
)

const ffmpegStderrLimit = 800

var logger = zap.NewNop().Sugar()

// SetLogger routes this package's logs into the bot's logger.
func SetLogger(l *zap.SugaredLogger) {
	logger = l
}

// cpuSlots bounds concurrent CPU-heavy processes (magick per image/frame, audio ffmpeg) across
// every job, so parallel videos and photos share the cores instead of oversubscribing them.
var cpuSlots = make(chan struct{}, runtime.NumCPU())

func acquireCPU() (release func()) {
	cpuSlots <- struct{}{}
	return func() { <-cpuSlots }
}

func runFfmpeg(args ...string) error {
	var outbuf, errbuf bytes.Buffer
	cmd := exec.Command("ffmpeg", args...)
	setProcessGroup(cmd)
	cmd.Stdout = &outbuf
	cmd.Stderr = &errbuf
	err := cmd.Run()
	if err != nil {
		stderr := truncateLog(errbuf.String(), ffmpegStderrLimit)
		last := lastNonEmptyLine(stderr)
		logger.Errorw("ffmpeg failed", "err", err, "args", strings.Join(args, " "), "stderr", stderr)
		if last != "" {
			return errors.Wrapf(err, "ffmpeg: %s", last)
		}
		return errors.WithStack(err)
	}
	return nil
}

func truncateLog(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return "…" + s[len(s)-limit:]
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}
	return ""
}
