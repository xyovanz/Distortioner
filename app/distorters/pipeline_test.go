package distorters

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireFfmpeg(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
}

func makeSource(t *testing.T, path string, withAudio bool) {
	t.Helper()
	args := []string{"-y", "-f", "lavfi", "-i", "testsrc=size=1920x1080:rate=10:duration=1"}
	if withAudio {
		args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-c:a", "aac")
	}
	args = append(args, "-c:v", "libx264", "-pix_fmt", "yuv420p", path)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("make source: %v\n%s", err, out)
	}
}

func probe(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "stream=codec_type,codec_name,width,height",
		"-of", "compact=p=0:nk=1", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	return strings.TrimSpace(string(out))
}

func runDistortVideo(t *testing.T, src, out string, audio *AudioOptions) {
	t.Helper()
	progress := make(chan string, 3)
	go DistortVideo(src, "libx264", out, 50, 0, 0, audio, progress)
	var last string
	for msg := range progress {
		last = msg
	}
	if IsFailureStatus(last) {
		t.Fatalf("DistortVideo failed: %s", last)
	}
}

// Without magick installed the per-frame distort is stubbed; this covers the ffmpeg side:
// frames are shrunk to the 512 box on extraction and audio is filtered in the same encode.
func TestDistortVideoPipeline(t *testing.T) {
	requireFfmpeg(t)
	orig := distortFrame
	t.Cleanup(func() { distortFrame = orig })
	distortFrame = func(string, int) error { return nil }

	dir := t.TempDir()
	withAudio := filepath.Join(dir, "a.mp4")
	silent := filepath.Join(dir, "s.mp4")
	makeSource(t, withAudio, true)
	makeSource(t, silent, false)

	out := filepath.Join(dir, "a_out.mp4")
	runDistortVideo(t, withAudio, out, &AudioOptions{Source: withAudio, Mode: SoundModeDistort, Intensity: 80})
	got := probe(t, out)
	if !strings.Contains(got, "h264|video|512|288") || !strings.Contains(got, "opus|audio") {
		t.Fatalf("unexpected streams with audio:\n%s", got)
	}

	// A source without an audio track must still encode when audio was requested.
	out = filepath.Join(dir, "s_out.mp4")
	runDistortVideo(t, silent, out, &AudioOptions{Source: silent, Mode: SoundModeBassBoost, Intensity: 50})
	got = probe(t, out)
	if got != "h264|video|512|288" {
		t.Fatalf("unexpected streams for silent source:\n%s", got)
	}

	// GIFs (no AudioOptions) get no audio stream.
	out = filepath.Join(dir, "gif_out.mp4")
	runDistortVideo(t, withAudio, out, nil)
	if got = probe(t, out); got != "h264|video|512|288" {
		t.Fatalf("unexpected streams for animation:\n%s", got)
	}
}
