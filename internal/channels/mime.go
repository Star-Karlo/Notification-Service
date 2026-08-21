package channels

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"mime"
	"strings"
	"time"

	"github.com/karlo/notification-service/internal/config"
)

// buildMIME assembles a multipart message.
//
// Headers are encoded rather than interpolated raw. A subject containing a
// newline would otherwise let a caller inject arbitrary headers, including Bcc.
func buildMIME(cfg config.Email, to, cc []string, subject, body string, attachments []Attachment) []byte {
	var buf bytes.Buffer

	boundary := "karlo-boundary-" + fmt.Sprint(time.Now().UnixNano())

	fmt.Fprintf(&buf, "From: %s <%s>\r\n", mime.QEncoding.Encode("utf-8", cfg.FromName), cfg.FromEmail)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(to, ", "))
	if len(cc) > 0 {
		fmt.Fprintf(&buf, "Cc: %s\r\n", strings.Join(cc, ", "))
	}
	fmt.Fprintf(&buf, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", sanitiseHeader(subject)))
	fmt.Fprintf(&buf, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	buf.WriteString("MIME-Version: 1.0\r\n")

	if len(attachments) == 0 {
		buf.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
		buf.WriteString(body)
		return buf.Bytes()
	}

	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", boundary)

	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	buf.WriteString("Content-Type: text/html; charset=UTF-8\r\n\r\n")
	buf.WriteString(body)
	buf.WriteString("\r\n")

	for _, a := range attachments {
		contentType := a.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		fmt.Fprintf(&buf, "--%s\r\n", boundary)
		fmt.Fprintf(&buf, "Content-Type: %s\r\n", contentType)
		buf.WriteString("Content-Transfer-Encoding: base64\r\n")
		fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=%q\r\n\r\n",
			sanitiseHeader(a.Filename))

		// Base64 bodies are wrapped at 76 characters, as RFC 2045 requires.
		encoded := base64.StdEncoding.EncodeToString(a.Content)
		for len(encoded) > 76 {
			buf.WriteString(encoded[:76])
			buf.WriteString("\r\n")
			encoded = encoded[76:]
		}
		buf.WriteString(encoded)
		buf.WriteString("\r\n")
	}

	fmt.Fprintf(&buf, "--%s--\r\n", boundary)
	return buf.Bytes()
}

// sanitiseHeader strips the characters that would let a value break out of its
// header and start a new one.
func sanitiseHeader(s string) string {
	return strings.NewReplacer("\r", "", "\n", "", "\x00", "").Replace(s)
}
