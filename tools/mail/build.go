package mail

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type BuildInput struct {
	From        string
	To          []string
	Cc          []string
	Bcc         []string
	Subject     string
	Body        string
	InReplyTo   string   // RFC Message-ID (without angle brackets) of message being replied to
	References  []string // chain of Message-IDs
	Attachments []string // local file paths
	Date        time.Time
	Hostname    string // for Message-ID generation; falls back to "darek.local"
}

type Built struct {
	Bytes     []byte
	MessageID string // generated; without angle brackets
}

func BuildMessage(in BuildInput) (Built, error) {
	if in.From == "" || len(in.To) == 0 {
		return Built{}, fmt.Errorf("from and to required")
	}
	if in.Date.IsZero() {
		in.Date = time.Now()
	}
	host := in.Hostname
	if host == "" {
		host = "darek.local"
	}
	mid, err := generateMessageID(host)
	if err != nil {
		return Built{}, err
	}

	var buf bytes.Buffer
	hdr := textproto.MIMEHeader{}
	hdr.Set("From", in.From)
	hdr.Set("To", strings.Join(in.To, ", "))
	if len(in.Cc) > 0 {
		hdr.Set("Cc", strings.Join(in.Cc, ", "))
	}
	hdr.Set("Subject", encodeHeader(in.Subject))
	hdr.Set("Date", in.Date.Format(time.RFC1123Z))
	hdr.Set("Message-ID", "<"+mid+">")
	if in.InReplyTo != "" {
		hdr.Set("In-Reply-To", "<"+strings.Trim(in.InReplyTo, "<>")+">")
	}
	if len(in.References) > 0 {
		var rs []string
		for _, r := range in.References {
			rs = append(rs, "<"+strings.Trim(r, "<>")+">")
		}
		hdr.Set("References", strings.Join(rs, " "))
	}
	hdr.Set("MIME-Version", "1.0")

	if len(in.Attachments) == 0 {
		hdr.Set("Content-Type", `text/plain; charset="utf-8"`)
		hdr.Set("Content-Transfer-Encoding", "8bit")
		writeHeaders(&buf, hdr)
		buf.WriteString("\r\n")
		buf.WriteString(in.Body)
		return Built{Bytes: buf.Bytes(), MessageID: mid}, nil
	}

	mw := multipart.NewWriter(&buf)
	hdr.Set("Content-Type", fmt.Sprintf(`multipart/mixed; boundary="%s"`, mw.Boundary()))
	writeHeaders(&buf, hdr)
	buf.WriteString("\r\n")

	// Plain-text body part.
	tphdr := textproto.MIMEHeader{}
	tphdr.Set("Content-Type", `text/plain; charset="utf-8"`)
	tphdr.Set("Content-Transfer-Encoding", "8bit")
	pw, err := mw.CreatePart(tphdr)
	if err != nil {
		return Built{}, fmt.Errorf("create text part: %w", err)
	}
	if _, err := pw.Write([]byte(in.Body)); err != nil {
		return Built{}, err
	}

	// Attachments.
	for _, path := range in.Attachments {
		if err := appendAttachment(mw, path); err != nil {
			return Built{}, fmt.Errorf("attach %s: %w", path, err)
		}
	}
	if err := mw.Close(); err != nil {
		return Built{}, err
	}
	return Built{Bytes: buf.Bytes(), MessageID: mid}, nil
}

func writeHeaders(w *bytes.Buffer, h textproto.MIMEHeader) {
	for k, vs := range h {
		for _, v := range vs {
			w.WriteString(k)
			w.WriteString(": ")
			w.WriteString(v)
			w.WriteString("\r\n")
		}
	}
}

func appendAttachment(mw *multipart.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	filename := filepath.Base(path)
	ct := mime.TypeByExtension(filepath.Ext(filename))
	if ct == "" {
		ct = "application/octet-stream"
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", fmt.Sprintf(`%s; name="%s"`, ct, filename))
	h.Set("Content-Transfer-Encoding", "base64")
	h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	pw, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	enc := base64.StdEncoding.EncodeToString(data)
	for i := 0; i < len(enc); i += 76 {
		end := i + 76
		if end > len(enc) {
			end = len(enc)
		}
		if _, err := pw.Write([]byte(enc[i:end] + "\r\n")); err != nil {
			return err
		}
	}
	return nil
}

func encodeHeader(s string) string {
	for _, r := range s {
		if r > 127 {
			return mime.QEncoding.Encode("utf-8", s)
		}
	}
	return s
}

func generateMessageID(host string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]) + "@" + host, nil
}
