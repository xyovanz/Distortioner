package tools

import (
	"encoding/json"
	"errors"
	"testing"

	tb "gopkg.in/telebot.v3"
)

func TestCanSendReplyMedia_GranularVsLegacy(t *testing.T) {
	photo := &tb.Message{Photo: &tb.Photo{}}
	video := &tb.Message{Video: &tb.Video{}}
	sticker := &tb.Message{Sticker: &tb.Sticker{}}

	// Modern getChat payload: granular flags only (can_send_media_messages omitted → false).
	modern := `{
		"can_send_messages": true,
		"can_send_photos": true,
		"can_send_videos": true,
		"can_send_video_notes": true,
		"can_send_voice_notes": true,
		"can_send_other_messages": true
	}`
	var modernPerms tb.Rights
	if err := json.Unmarshal([]byte(modern), &modernPerms); err != nil {
		t.Fatal(err)
	}
	if modernPerms.CanSendMedia {
		t.Fatal("expected deprecated CanSendMedia to be false on modern JSON")
	}
	if !CanSendReplyMedia(&modernPerms, photo) || !CanSendReplyMedia(&modernPerms, video) {
		t.Fatal("modern granular allow must permit photo/video")
	}

	// Legacy payload: only can_send_media_messages.
	legacy := `{"can_send_messages": true, "can_send_media_messages": true, "can_send_other_messages": true}`
	var legacyPerms tb.Rights
	if err := json.Unmarshal([]byte(legacy), &legacyPerms); err != nil {
		t.Fatal(err)
	}
	if !CanSendReplyMedia(&legacyPerms, photo) {
		t.Fatal("legacy CanSendMedia must still permit photo")
	}

	denied := &tb.Rights{CanSendMessages: true}
	if CanSendReplyMedia(denied, photo) || CanSendReplyMedia(denied, sticker) {
		t.Fatal("denied media/other permissions must reject")
	}
	if !CanSendReplyMedia(denied, &tb.Message{Text: "hi"}) {
		t.Fatal("text replies do not need media send rights")
	}
}

func TestExtractPossibleTimeout(t *testing.T) {
	err := errors.New("telegram: retry after 5 (429)")
	timeout, err := ExtractPossibleTimeout(err)
	if timeout != 5 || err != nil {
		t.Fatal(err)
	}
	err = errors.New("telegram: retry after 10 (429)")
	timeout, err = ExtractPossibleTimeout(err)
	if timeout != 10 || err != nil {
		t.Fatal(err)
	}

	err = errors.New("retry after 24 (429)")
	timeout, err = ExtractPossibleTimeout(err)
	if timeout != 24 || err != nil {
		t.Fatal(err)
	}

	err = errors.New("retry after 50")
	timeout, err = ExtractPossibleTimeout(err)
	if timeout != 50 || err != nil {
		t.Fatal(err)
	}
	err = errors.New("retry after")
	timeout, err = ExtractPossibleTimeout(err)
	if timeout != 0 || err == nil {
		t.Fatal(err)
	}
	err = errors.New("telegram: bot was kicked from the supergroup chat (403)")
	timeout, err = ExtractPossibleTimeout(err)
	if timeout != 0 || err == nil {
		t.Fatal(err)
	}
}
