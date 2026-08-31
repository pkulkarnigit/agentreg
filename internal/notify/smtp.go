package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// smtpTimeout bounds an entire send (dial through QUIT) so a slow or
// hanging mail server can't tie up the HTTP request goroutine that
// triggered it indefinitely — net/smtp's own SendMail has no such limit,
// which is exactly why this doesn't use it.
const smtpTimeout = 15 * time.Second

// SMTPSender delivers over plain SMTP with opportunistic STARTTLS and
// optional AUTH — the lowest-common-denominator protocol nearly every
// transactional email provider speaks (SendGrid, SES, Mailgun, Postmark,
// or a self-hosted MTA), so this isn't tied to any one vendor's SDK.
// Swapping providers is a config change, not a code change.
type SMTPSender struct {
	Host     string // e.g. smtp.sendgrid.net
	Port     int    // e.g. 587
	Username string // optional: leave empty to skip AUTH (e.g. an unauthenticated local relay)
	Password string
	From     string // header From, e.g. "AgentReg <noreply@yourdomain.com>"
}

func (s SMTPSender) Send(ctx context.Context, to, subject, body string) error {
	fromHeader := s.From
	if fromHeader == "" {
		fromHeader = s.Username
	}
	fromAddr, err := mail.ParseAddress(fromHeader)
	if err != nil {
		return fmt.Errorf("notify: invalid From address %q: %w", fromHeader, err)
	}

	var msg strings.Builder
	fmt.Fprintf(&msg, "From: %s\r\n", fromHeader)
	fmt.Fprintf(&msg, "To: %s\r\n", to)
	fmt.Fprintf(&msg, "Subject: %s\r\n", subject)
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body)

	deadline := time.Now().Add(smtpTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	conn, err := net.DialTimeout("tcp", addr, smtpTimeout)
	if err != nil {
		return fmt.Errorf("notify: dial %s: %w", addr, err)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return fmt.Errorf("notify: set deadline: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return fmt.Errorf("notify: smtp handshake: %w", err)
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: s.Host}); err != nil {
			return fmt.Errorf("notify: starttls: %w", err)
		}
	}

	if s.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return fmt.Errorf("notify: auth: %w", err)
		}
	}

	if err := client.Mail(fromAddr.Address); err != nil {
		return fmt.Errorf("notify: MAIL FROM: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("notify: RCPT TO: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("notify: DATA: %w", err)
	}
	if _, err := w.Write([]byte(msg.String())); err != nil {
		return fmt.Errorf("notify: write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("notify: close message: %w", err)
	}
	return client.Quit()
}

var _ Sender = SMTPSender{}
