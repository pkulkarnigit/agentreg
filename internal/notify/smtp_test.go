package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSMTPSender_Send delivers a real message through a real SMTP server
// (Mailpit, an SMTP test double with an HTTP API for inspecting what it
// received) and confirms the message that actually arrived — not just that
// Send() returned nil — has the right recipient, subject, and body.
// Requires APREG_TEST_SMTP_ADDR (SMTP) and APREG_TEST_SMTP_HTTP_ADDR
// (Mailpit's API); skips cleanly without them.
func TestSMTPSender_Send(t *testing.T) {
	smtpAddr := os.Getenv("APREG_TEST_SMTP_ADDR")
	httpAddr := os.Getenv("APREG_TEST_SMTP_HTTP_ADDR")
	if smtpAddr == "" || httpAddr == "" {
		t.Skip("APREG_TEST_SMTP_ADDR/APREG_TEST_SMTP_HTTP_ADDR not set; skipping SMTP conformance test")
	}

	host, portStr, err := net.SplitHostPort(smtpAddr)
	if err != nil {
		t.Fatalf("parse APREG_TEST_SMTP_ADDR: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	sender := SMTPSender{Host: host, Port: port, From: "AgentReg <noreply@agentreg.test>"}

	to := fmt.Sprintf("recipient-%d@example.com", time.Now().UnixNano())
	subject := "Verify your AgentReg account"
	body := "Confirm your AgentReg account:\n\n  apreg verify-email sometoken\n\nThis link expires in 24 hours."

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sender.Send(ctx, to, subject, body); err != nil {
		t.Fatalf("Send: %v", err)
	}

	msg := findMessage(t, httpAddr, to)
	if msg.Subject != subject {
		t.Fatalf("subject mismatch: got %q, want %q", msg.Subject, subject)
	}
	if !strings.Contains(msg.Text, "Confirm your AgentReg account") {
		t.Fatalf("body not delivered correctly: got %q", msg.Text)
	}
	if msg.From.Address != "noreply@agentreg.test" {
		t.Fatalf("From address mismatch: got %q", msg.From.Address)
	}
}

type mailpitMessage struct {
	Subject string `json:"Subject"`
	Text    string `json:"Text"`
	From    struct {
		Address string `json:"Address"`
	} `json:"From"`
}

type mailpitSearchResult struct {
	Messages []struct {
		ID string `json:"ID"`
	} `json:"messages"`
}

// findMessage polls Mailpit's API briefly for a message to the given
// recipient — delivery through a real SMTP round trip isn't instantaneous
// even locally.
func findMessage(t *testing.T, httpAddr, to string) mailpitMessage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("http://%s/api/v1/search?query=to:%s", httpAddr, to))
		if err == nil {
			var result mailpitSearchResult
			if json.NewDecoder(resp.Body).Decode(&result) == nil && len(result.Messages) == 1 {
				resp.Body.Close()
				msgResp, err := http.Get(fmt.Sprintf("http://%s/api/v1/message/%s", httpAddr, result.Messages[0].ID))
				if err != nil {
					t.Fatalf("fetch message: %v", err)
				}
				defer msgResp.Body.Close()
				var msg mailpitMessage
				if err := json.NewDecoder(msgResp.Body).Decode(&msg); err != nil {
					t.Fatalf("decode message: %v", err)
				}
				return msg
			}
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no message to %s arrived within the deadline", to)
	return mailpitMessage{}
}
