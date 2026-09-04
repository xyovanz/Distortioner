package distorters

import (
	"fmt"
	"os"

	"github.com/pkg/errors"
)

// DistortVideoSticker rasterizes a VP9/webm sticker, applies progressive liquid-rescale, and re-encodes webm+alpha.
func DistortVideoSticker(filename, output string, intensity int) error {
	framesDir := filename + "Frames"
	err := os.Mkdir(framesDir, 0755)
	if err != nil {
		return errors.WithStack(err)
	}
	defer os.RemoveAll(framesDir)

	frameRateFraction, duration, err := GetFrameRateFractionAndDuration(filename)
	if err != nil {
		return errors.Wrap(err, FailedProbe)
	}
	if duration > 30 {
		return errors.New(TooLong)
	}

	numberedFileName := fmt.Sprintf("%s/%s%%04d.png", framesDir, filename)
	err = extractFramesFromVideoSticker(frameRateFraction, filename, numberedFileName)
	if err != nil {
		return errors.Wrap(err, FailedExtract)
	}

	distortedFrames := 0
	doneChan := make(chan int, 8)
	go poolDistortImages(framesDir, doneChan, intensity)

	for totalFrames := <-doneChan; distortedFrames != totalFrames; {
		framesDistorted := <-doneChan
		if framesDistorted == -1 {
			return errors.New(FailedDistortImage)
		}
		distortedFrames += framesDistorted
	}

	err = collectFramesToVideoSticker(numberedFileName, frameRateFraction, output)
	if err != nil {
		return errors.Wrap(err, FailedEncode)
	}
	return nil
}
