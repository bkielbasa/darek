package whatsapp

import (
	"fmt"

	"go.mau.fi/whatsmeow/types/events"
)

// decodeMessage maps a whatsmeow *events.Message to (kind, body) for
// persistence. Pure: no I/O. Unknown shapes fall through to
// ("other", "[unsupported message type]"). The "kind" values are the soft
// enum stored in whatsapp_messages.kind:
// text|image|voice|audio|video|document|sticker|other.
func decodeMessage(msg *events.Message) (kind, body string) {
	if msg == nil || msg.Message == nil {
		return "other", "[unsupported message type]"
	}
	m := msg.Message

	switch {
	case m.Conversation != nil && *m.Conversation != "":
		return "text", *m.Conversation

	case m.ExtendedTextMessage != nil && m.ExtendedTextMessage.Text != nil:
		return "text", strDeref(m.ExtendedTextMessage.Text)

	case m.ImageMessage != nil:
		caption := strDeref(m.ImageMessage.Caption)
		if caption != "" {
			return "image", "[image] " + caption
		}
		return "image", "[image]"

	case m.VideoMessage != nil:
		caption := strDeref(m.VideoMessage.Caption)
		if caption != "" {
			return "video", "[video] " + caption
		}
		return "video", "[video]"

	case m.AudioMessage != nil:
		secs := uint32Deref(m.AudioMessage.Seconds)
		if boolDeref(m.AudioMessage.PTT) {
			return "voice", fmt.Sprintf("[voice %ds]", secs)
		}
		return "audio", "[audio]"

	case m.DocumentMessage != nil:
		name := strDeref(m.DocumentMessage.FileName)
		if name == "" {
			return "document", "[document]"
		}
		return "document", "[document: " + name + "]"

	case m.StickerMessage != nil:
		return "sticker", "[sticker]"
	}

	return "other", "[unsupported message type]"
}

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func boolDeref(p *bool) bool {
	return p != nil && *p
}

func uint32Deref(p *uint32) uint32 {
	if p == nil {
		return 0
	}
	return *p
}
