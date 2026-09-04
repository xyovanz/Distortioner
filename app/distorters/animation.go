package distorters

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/graynk/distortioner/tools"
)

const (
	Failed             = "Failed to process media"
	FailedProbe        = "Failed to read video"
	FailedExtract      = "Failed to extract video frames"
	FailedDistortImage = "Failed to distort image"
	FailedEncode       = "Failed to encode video"
	FailedDistortAudio = "Failed to distort audio"
	FailedDownload     = "Failed to download media"
	TooLong            = "Senpai, it's too long.."
	TooBig             = "Senpai, it's too big.."
	Queued             = "Your message has been queued"
)

func FormatQueued(position int) string {
	if position < 1 {
		return Queued
	}
	return fmt.Sprintf("%s (position %d)", Queued, position)
}

func IsFailureStatus(text string) bool {
	switch text {
	case Failed, FailedProbe, FailedExtract, FailedDistortImage, FailedEncode, FailedDistortAudio, FailedDownload, TooLong, TooBig:
		return true
	default:
		return false
	}
}

// UserFacingError maps an error to a short Telegram-facing status string.
func UserFacingError(err error) string {
	if err == nil {
		return Failed
	}
	msg := err.Error()
	switch {
	case msg == TooLong || strings.Contains(msg, TooLong):
		return TooLong
	case msg == TooBig || strings.Contains(msg, TooBig):
		return TooBig
	case strings.Contains(msg, FailedProbe), strings.Contains(msg, "ffprobe"):
		return FailedProbe
	case strings.Contains(msg, FailedExtract):
		return FailedExtract
	case strings.Contains(msg, FailedDistortImage), strings.Contains(msg, "magick"), strings.Contains(msg, "liquid"):
		return FailedDistortImage
	case strings.Contains(msg, FailedEncode), strings.Contains(msg, "libx264"), strings.Contains(msg, "encode"):
		return FailedEncode
	case strings.Contains(msg, FailedDistortAudio), strings.Contains(msg, "vibrato"):
		return FailedDistortAudio
	case strings.Contains(msg, "download"), strings.Contains(msg, FailedDownload):
		return FailedDownload
	case IsFailureStatus(msg):
		return msg
	default:
		return Failed
	}
}

func DistortVideo(filename, codec, output string, intensity, rampFrom, rampTo int, progressChan chan string) {
	progressChan <- "Extracting frames..."
	defer close(progressChan)
	framesDir := filename + "Frames"
	err := os.Mkdir(framesDir, 0755)
	if err != nil {
		err = errors.WithStack(err)
		log.Println(err)
		progressChan <- Failed
		return
	}
	defer os.RemoveAll(framesDir)
	frameRateFraction, duration, err := GetFrameRateFractionAndDuration(filename)
	if err != nil {
		progressChan <- FailedProbe
		return
	} else if duration > 60 {
		progressChan <- TooLong
		return
	}
	numberedFileName := fmt.Sprintf("%s/%s%%04d.png", framesDir, filename)
	err = extractFramesFromVideo(frameRateFraction, filename, numberedFileName)
	if err != nil {
		progressChan <- FailedExtract
		return
	}

	distortedFrames := 0
	doneChan := make(chan int, 8)
	go poolDistortImages(framesDir, doneChan, intensity, rampFrom, rampTo)

	lastUpdate := time.Now()
	for totalFrames := <-doneChan; distortedFrames != totalFrames; {
		framesDistorted := <-doneChan
		if framesDistorted == -1 {
			progressChan <- FailedDistortImage
			return
		}
		distortedFrames += framesDistorted
		now := time.Now()
		if now.Sub(lastUpdate).Seconds() > 2 {
			lastUpdate = now
			progressChan <- tools.GenerateProgressMessage(distortedFrames, totalFrames)
		}
	}
	progressChan <- "Collecting frames..."
	err = collectFramesToVideo(numberedFileName, frameRateFraction, codec, output)
	if err != nil {
		progressChan <- FailedEncode
	}
	return
}

func GetFrameRateFractionAndDuration(filename string) (string, float64, error) {
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-select_streams", "v",
		"-of", "default=noprint_wrappers=1:nokey=1",
		"-show_entries", "stream=avg_frame_rate",
		"-show_entries", "format=duration",
		filename)
	setProcessGroup(cmd)
	var errbuf bytes.Buffer
	cmd.Stderr = &errbuf
	output, err := cmd.Output()
	if err != nil {
		stderr := truncateLog(errbuf.String(), ffmpegStderrLimit)
		last := lastNonEmptyLine(stderr)
		log.Printf("ffprobe failed exit=%v file=%q stderr=%q", err, filename, stderr)
		if last != "" {
			return "", 0, errors.Wrapf(err, "ffprobe: %s", last)
		}
		return "", 0, errors.WithStack(err)
	}
	split := strings.Split(string(output), "\n")
	if len(split) < 2 {
		return "", 0, errors.New(FailedProbe)
	}
	duration, err := strconv.ParseFloat(split[1], 32)
	if err != nil {
		err = errors.WithStack(err)
		log.Println(err)
	}
	return split[0], duration, err
}

func extractFramesFromVideo(frameRateFraction, filename, numberedFileName string) error {
	return runFfmpeg("-i", filename,
		"-r", frameRateFraction,
		numberedFileName)
}

func extractFramesFromVideoSticker(frameRateFraction, filename, numberedFileName string) error {
	return runFfmpeg("-vcodec", "libvpx-vp9",
		"-i", filename,
		"-r", frameRateFraction,
		"-pix_fmt", "rgba",
		numberedFileName)
}

func collectFramesToVideo(numberedFileName, frameRateFraction, codec, filename string) error {
	// libx264 + yuv420p requires even width/height; liquid-rescale can leave odd sizes (e.g. 383x383).
	return runFfmpeg("-r", frameRateFraction,
		"-i", numberedFileName,
		"-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2",
		"-f", "mp4",
		"-c:v", codec,
		"-an",
		"-pix_fmt", "yuv420p",
		filename)
}

func collectFramesToVideoSticker(numberedFileName, frameRateFraction, filename string) error {
	return runFfmpeg("-r", frameRateFraction,
		"-i", numberedFileName,
		"-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2",
		"-f", "webm",
		"-c:v", "libvpx-vp9",
		"-b:v", "85k", // ebuchiy shakal mode activated
		"-an",
		"-pix_fmt", "yuva420p",
		filename)
}

func poolDistortImages(frameDir string, doneChan chan int, intensity, rampFrom, rampTo int) {
	cpuCount := runtime.NumCPU()
	sem := make(chan bool, cpuCount)
	frames, err := os.ReadDir(frameDir)
	if err != nil {
		doneChan <- -1
		doneChan <- -1
		return
	}
	sort.Slice(frames, func(i, j int) bool {
		return frames[i].Name() < frames[j].Name()
	})
	total := len(frames)
	doneChan <- total
	for i, frame := range frames {
		sem <- true
		go func(i int, frame string) {
			defer func() {
				<-sem
				doneChan <- 1
			}()
			frameIntensity := FrameIntensity(intensity, rampFrom, rampTo, i, total)
			err := DistortImage(fmt.Sprintf("%s/%s", frameDir, frame), frameIntensity)
			if err != nil {
				doneChan <- -1
			}
		}(i, frame.Name())
	}
}
