package channels

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/karlo/notification-service/internal/config"
	"github.com/karlo/notification-service/internal/models"
)

// Email sends mail over SMTP.
//
// This replaces the nodemailer calls that were made directly from five
// controllers in the monolith, each with its own copy of the connection
// details and its own idea of what the message should look like.
type Email struct {
	cfg config.Email
}

func NewEmail(cfg config.Email) *Email { return &Email{cfg: cfg} }

func (e *Email) Channel() models.Channel { return models.ChannelEmail }

func (e *Email) Enabled() bool { return e.cfg.Enabled }

// Attachment is a file to include with a message.
type Attachment struct {
	Filename    string
	ContentType string
	Content     []byte
}

// Send delivers a plain notification email.
func (e *Email) Send(ctx context.Context, msg Message) error {
	return e.SendWithAttachments(ctx, []string{msg.Target}, nil, msg.Title, msg.Body, nil)
}

// SendWithAttachments delivers a document email, which is what invoices and
// proof-of-delivery notices need.
func (e *Email) SendWithAttachments(ctx context.Context, to, cc []string, subject, body string, attachments []Attachment) error {
	if !e.Enabled() {
		return ErrChannelDisabled
	}

	recipients := append(append([]string{}, to...), cc...)
	if len(recipients) == 0 {
		return &InvalidTargetError{Reason: "no recipients"}
	}
	for _, addr := range recipients {
		if !strings.Contains(addr, "@") {
			return &InvalidTargetError{Target: addr, Reason: "not an email address"}
		}
	}

	payload := buildMIME(e.cfg, to, cc, subject, body, attachments)

	// Dial with the context's deadline so a hung SMTP server cannot block a
	// delivery worker indefinitely.
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}

	addr := net.JoinHostPort(e.cfg.Host, fmt.Sprint(e.cfg.Port))
	conn, err := (&net.Dialer{Deadline: deadline}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("channels: dial smtp %s: %w", addr, err)
	}

	client, err := smtp.NewClient(conn, e.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("channels: smtp handshake: %w", err)
	}
	defer func() { _ = client.Quit() }()

	// STARTTLS where the server offers it. Credentials must not cross the
	// network in clear.
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: e.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("channels: starttls: %w", err)
		}
	}

	if e.cfg.Username != "" {
		auth := smtp.PlainAuth("", e.cfg.Username, e.cfg.Password, e.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("channels: smtp auth: %w", err)
		}
	}

	if err := client.Mail(e.cfg.FromEmail); err != nil {
		return fmt.Errorf("channels: smtp from: %w", err)
	}
	for _, addr := range recipients {
		if err := client.Rcpt(addr); err != nil {
			return fmt.Errorf("channels: smtp rcpt %s: %w", addr, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("channels: smtp data: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		_ = w.Close()
		return fmt.Errorf("channels: smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("channels: smtp close: %w", err)
	}

	return nil
}
