package distorters

import (
	"fmt"
	"os"

	"github.com/pkg/errors"
)

// DistortVideoSticker rasterizes a VP9/webm sticker, applies liquid-rescale per frame, and re-encodes webm+alpha.
func DistortVideoSticker(filename, output string, intensity, rampFrom, rampTo int) error {
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
	go poolDistortImages(framesDir, doneChan, intensity, rampFrom, rampTo)

	for totalFrames := <-doneChan; distortedFrames != totalFrames; {
		framesDistorted := <-doneChan
		if framesDistorted == -1 {
			drainFrameSignals(doneChan, distortedFrames+1, totalFrames)
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
