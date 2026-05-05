package whatsapp

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func msgWith(m *waE2E.Message) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   types.JID{User: "g1", Server: types.GroupServer},
				Sender: types.JID{User: "1234", Server: types.DefaultUserServer},
			},
			ID:        "MID1",
			Timestamp: time.Now().UTC(),
			PushName:  "Bart",
		},
		Message: m,
	}
}

func TestDecodeMessage(t *testing.T) {
	cases := []struct {
		name         string
		msg          *waE2E.Message
		wantKind     string
		wantBody     string
		bodyContains bool // when true, treat wantBody as substring
	}{
		{
			name:     "plain text via Conversation",
			msg:      &waE2E.Message{Conversation: strPtr("hello world")},
			wantKind: "text",
			wantBody: "hello world",
		},
		{
			name: "extended text",
			msg: &waE2E.Message{
				ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: strPtr("link preview text")},
			},
			wantKind: "text",
			wantBody: "link preview text",
		},
		{
			name: "image without caption",
			msg: &waE2E.Message{
				ImageMessage: &waE2E.ImageMessage{},
			},
			wantKind: "image",
			wantBody: "[image]",
		},
		{
			name: "image with caption",
			msg: &waE2E.Message{
				ImageMessage: &waE2E.ImageMessage{Caption: strPtr("look at this")},
			},
			wantKind:     "image",
			wantBody:     "look at this",
			bodyContains: true,
		},
		{
			name: "video",
			msg: &waE2E.Message{
				VideoMessage: &waE2E.VideoMessage{},
			},
			wantKind: "video",
			wantBody: "[video]",
		},
		{
			name: "voice (PTT)",
			msg: &waE2E.Message{
				AudioMessage: &waE2E.AudioMessage{
					PTT:     boolPtr(true),
					Seconds: u32Ptr(12),
				},
			},
			wantKind: "voice",
			wantBody: "[voice 12s]",
		},
		{
			name: "audio (non-PTT)",
			msg: &waE2E.Message{
				AudioMessage: &waE2E.AudioMessage{PTT: boolPtr(false)},
			},
			wantKind: "audio",
			wantBody: "[audio]",
		},
		{
			name: "document with filename",
			msg: &waE2E.Message{
				DocumentMessage: &waE2E.DocumentMessage{FileName: strPtr("report.pdf")},
			},
			wantKind:     "document",
			wantBody:     "report.pdf",
			bodyContains: true,
		},
		{
			name: "sticker",
			msg: &waE2E.Message{
				StickerMessage: &waE2E.StickerMessage{},
			},
			wantKind: "sticker",
			wantBody: "[sticker]",
		},
		{
			name:     "unknown / nothing populated",
			msg:      &waE2E.Message{},
			wantKind: "other",
			wantBody: "[unsupported message type]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, body := decodeMessage(msgWith(tc.msg))
			require.Equal(t, tc.wantKind, kind)
			if tc.bodyContains {
				require.Contains(t, body, tc.wantBody)
			} else {
				require.Equal(t, tc.wantBody, body)
			}
		})
	}
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }
func u32Ptr(v uint32) *uint32 { return &v }
