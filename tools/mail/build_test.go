package mail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildMessage_PlainNoAttach(t *testing.T) {
	got, err := BuildMessage(BuildInput{
		From: "me@x.com", To: []string{"a@x.com"},
		Subject: "Hi", Body: "hello\nworld",
	})
	require.NoError(t, err)
	s := string(got.Bytes)
	require.Contains(t, s, "From: me@x.com")
	require.Contains(t, s, "To: a@x.com")
	require.Contains(t, s, "Subject: Hi")
	require.Contains(t, s, "Content-Type: text/plain")
	require.Contains(t, s, "hello\nworld")
	require.NotEmpty(t, got.MessageID)
}

func TestBuildMessage_ReplyHeaders(t *testing.T) {
	got, err := BuildMessage(BuildInput{
		From: "me@x.com", To: []string{"a@x.com"},
		Subject: "Re: x", Body: "y",
		InReplyTo: "abc@host", References: []string{"first@host", "abc@host"},
	})
	require.NoError(t, err)
	s := string(got.Bytes)
	require.Contains(t, s, "In-Reply-To: <abc@host>")
	require.Contains(t, s, "References: <first@host> <abc@host>")
}

func TestBuildMessage_WithAttachment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.pdf")
	require.NoError(t, os.WriteFile(path, []byte("PDF-BYTES"), 0o600))
	got, err := BuildMessage(BuildInput{
		From: "me@x.com", To: []string{"a@x.com"},
		Subject: "x", Body: "hi",
		Attachments: []string{path},
	})
	require.NoError(t, err)
	s := string(got.Bytes)
	require.Contains(t, s, "multipart/mixed; boundary=")
	require.Contains(t, s, `Content-Disposition: attachment; filename="doc.pdf"`)
	require.Contains(t, s, "UERGLUJZVEVT") // base64 of "PDF-BYTES"
}

func TestBuildMessage_RequiresFromAndTo(t *testing.T) {
	_, err := BuildMessage(BuildInput{Subject: "x", Body: "y"})
	require.Error(t, err)
	_, err = BuildMessage(BuildInput{From: "x@y", Subject: "x", Body: "y"})
	require.Error(t, err)
}

func TestBuildMessage_NonASCIISubjectQEncoded(t *testing.T) {
	got, err := BuildMessage(BuildInput{From: "me@x", To: []string{"a@x"}, Subject: "héllo", Body: "y"})
	require.NoError(t, err)
	s := string(got.Bytes)
	require.True(t, strings.Contains(s, "=?utf-8?") && strings.Contains(s, "?="))
}
