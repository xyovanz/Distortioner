package distorters

import (
	"bytes"
	"log"
	"os/exec"
	"strings"

	"github.com/pkg/errors"
)

const ffmpegStderrLimit = 800

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
		log.Printf("ffmpeg failed exit=%v args=%q stderr=%q", err, strings.Join(args, " "), stderr)
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
