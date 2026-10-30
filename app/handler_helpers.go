package main

import (
	"errors"
	"os"
	"strings"
	"time"

	tb "gopkg.in/telebot.v3"

	"github.com/graynk/distortioner/distorters"
	"github.com/graynk/distortioner/stats"
	"github.com/graynk/distortioner/tools"
)

const (
	NotEnoughRights             = "The bot does not have enough rights to send media to your chat"
	AnimatedStickersUnsupported = "Animated stickers (.tgs) aren't supported — send a static or video sticker instead."
)

type MethodOfResponding = int

const (
	Reply MethodOfResponding = iota
	Send
)

// HandleAnimationCommon downloads and distorts a GIF/video/video note, reporting progress in a
// reply. withAudio also runs the source's audio through the user's sound filter in the same encode.
func (d DistorterBot) HandleAnimationCommon(c tb.Context, s stats.Settings, withAudio bool) (*tb.Message, string, string, error) {
	m := c.Message()
	b := c.Bot()
	progressMessage, err := d.SendMessage(c, "Downloading...", Reply)
	if err != nil {
		d.logger.Error(err)
		return nil, "", "", err
	}
	filename, err := tools.JustGetTheFile(b, m)
	if err != nil {
		d.logger.Error(err)
		return progressMessage, "", "", errors.New(distorters.FailedDownload)
	}
	animationOutput := filename + ".mp4"
	var audio *distorters.AudioOptions
	if withAudio {
		audio = &distorters.AudioOptions{Source: filename, Mode: s.SoundMode, Intensity: s.Intensity}
	}
	progressChan := make(chan string, 3)
	go distorters.DistortVideo(filename, d.codec, animationOutput, s.Intensity, s.RampFrom, s.RampTo, audio, progressChan)
	for report := range progressChan {
		if progressMessage == nil {
			continue
		}
		msg, err := b.Edit(progressMessage, report, &tb.SendOptions{ParseMode: tb.ModeHTML})
		if err == nil && msg != nil {
			progressMessage = msg
		}
	}
	_, err = os.Stat(animationOutput)
	if err != nil {
		if progressMessage != nil && distorters.IsFailureStatus(progressMessage.Text) {
			return progressMessage, filename, animationOutput, errors.New(progressMessage.Text)
		}
		return progressMessage, filename, animationOutput, errors.New(distorters.FailedEncode)
	}
	return progressMessage, filename, animationOutput, nil
}

func (d DistorterBot) HandleVideoSticker(c tb.Context, s stats.Settings) (string, string, error) {
	filename, err := tools.JustGetTheFile(c.Bot(), c.Message())
	if err != nil {
		d.logger.Error(err)
		return "", "", errors.New(distorters.FailedDownload)
	}
	animationOutput := filename + ".webm"
	err = distorters.DistortVideoSticker(filename, animationOutput, s.Intensity, s.RampFrom, s.RampTo)
	if err != nil {
		return filename, animationOutput, err
	}
	if _, err = os.Stat(animationOutput); err != nil {
		return filename, animationOutput, errors.New(distorters.FailedEncode)
	}
	return filename, animationOutput, nil
}

func (d DistorterBot) dealWithStatusMessage(b *tb.Bot, m *tb.Message, failMsg string) error {
	if m == nil {
		return nil
	}
	var err error
	if failMsg != "" {
		_, err = b.Edit(m, failMsg)
	} else {
		err = b.Delete(m)
	}
	return err
}

// DoneMessageWithRepeater deletes the progress message on success, or edits it to failMsg on failure.
func (d DistorterBot) DoneMessageWithRepeater(b *tb.Bot, m *tb.Message, failMsg string) {
	err := d.dealWithStatusMessage(b, m, failMsg)
	for err != nil {
		var timeout int
		timeout, err = tools.ExtractPossibleTimeout(err)
		if err != nil {
			return
		}
		time.Sleep(time.Duration(timeout) * time.Second)
		err = d.dealWithStatusMessage(b, m, failMsg)
	}
}

func (d DistorterBot) SendMessage(c tb.Context, toSend interface{}, method MethodOfResponding) (*tb.Message, error) {
	b := c.Bot()
	message := c.Message()
	send := func() (*tb.Message, error) {
		if method == Reply {
			return b.Reply(message, toSend)
		}
		return b.Send(message.Chat, toSend)
	}

	m, err := send()
	for err != nil {
		switch {
		case strings.Contains(err.Error(), "not enough rights to send"):
			b.Reply(message, NotEnoughRights)
		case strings.Contains(err.Error(), "bot was blocked by the user (403)"):
			d.videoWorker.BanUser(message.Chat.ID)
			return nil, nil
		case strings.Contains(err.Error(), "telegram: Bad Request: message to be replied not found (400)"):
			return d.SendMessage(c, toSend, Send)
		}

		var timeout int
		timeout, err = tools.ExtractPossibleTimeout(err)
		if err != nil {
			d.logger.Error(err)
			return nil, err
		}
		time.Sleep(time.Duration(timeout) * time.Second)
		// Retry the same way: a Send fallback must not turn back into a Reply.
		m, err = send()
		if err != nil {
			d.logger.Error(err)
		}
	}

	return m, nil
}

func (d DistorterBot) SendMessageWithRepeater(c tb.Context, toSend interface{}) error {
	_, err := d.SendMessage(c, toSend, Reply)
	return err
}

func (d DistorterBot) ApplyShutdownMiddleware(h tb.HandlerFunc) tb.HandlerFunc {
	return func(c tb.Context) error {
		d.graceWg.Add(1)
		// Must defer: telebot's Recover middleware swallows panics, so without
		// this a panic permanently leaks the wait group and hangs graceful shutdown.
		defer d.graceWg.Done()
		return h(c)
	}
}
