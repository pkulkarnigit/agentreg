// Package notify defines how AgentReg delivers account-recovery messages
// (email verification, password reset links). Real delivery needs an
// SMTP/SES provider and a domain, neither of which exist for local/Docker
// use — so the interface is built now and a real implementation is a new
// file behind it later, the same swap pattern as internal/store.
package notify

import (
	"context"
	"log/slog"
)

// Sender delivers a message to an address. Implementations decide what
// "delivering" means — an SMTP send, a queued job, or (for local dev) just
// logging it.
type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// LogSender "delivers" by writing the message to the server log. This is
// what AgentReg uses today: there's no mail provider or domain wired up
// yet, so this is what makes email verification / password reset usable in
// local/Docker dev without one — the link is right there in the logs.
// Swapping in real SMTP/SES delivery later means implementing Sender in a
// new file, not touching any caller of this interface.
type LogSender struct {
	Logger *slog.Logger
}

func (s LogSender) Send(_ context.Context, to, subject, body string) error {
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("notify: (no mail provider configured — logging instead)",
		"to", to, "subject", subject, "body", body)
	return nil
}

var _ Sender = LogSender{}
