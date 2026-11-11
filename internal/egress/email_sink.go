// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// email_sink.go is the built-in SMTP email sink. It renders the envelope as
// readable JSON in the email body and sends it through internal/notify's
// context-aware SMTP path, so the worker's hard delivery deadline bounds the
// dial and the whole protocol exchange. SMTP failures are conservatively
// treated as retryable (rather than dropping a message) until retries are
// exhausted.

package egress

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nereidai/nereidlink/internal/notify"
)

// emailConfig is the email sink configuration.
type emailConfig struct {
	// Host/Port/From identify the SMTP server and envelope sender.
	Host string `json:"host"`
	Port int    `json:"port"`
	From string `json:"from"`
	// Security is "starttls" (default, typically 587), "tls" (implicit
	// TLS, typically 465) or "none" (plaintext, typically 25).
	Security string `json:"security"`
	// EHLODomain overrides the FQDN announced in EHLO/HELO; empty
	// defaults to the domain part of From.
	EHLODomain string `json:"ehlo_domain"`
	// UseAuth enables SMTP AUTH with Username/Password; when false the
	// message is sent anonymously (open relay / trusted network).
	UseAuth bool `json:"use_auth"`
	// Username/Password authenticate when UseAuth is true.
	Username string `json:"username"`
	Password string `json:"password"`
	// To lists the recipient addresses.
	To []string `json:"to"`
	// Subject overrides the default subject when set.
	Subject string `json:"subject"`
}

// emailSink sends envelopes as SMTP messages.
type emailSink struct {
	cfg emailConfig
}

func init() {
	Register(SinkEmail, func(raw map[string]any) (Sink, error) {
		buf, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var cfg emailConfig
		if err := json.Unmarshal(buf, &cfg); err != nil {
			return nil, err
		}
		if cfg.Host == "" {
			return nil, fmt.Errorf("email sink requires host")
		}
		if cfg.From == "" {
			return nil, fmt.Errorf("email sink requires from")
		}
		if len(cfg.To) == 0 {
			return nil, fmt.Errorf("email sink requires at least one recipient")
		}
		// Default the port from the chosen transport mode when unset.
		switch cfg.Security {
		case "", notify.SMTPSecuritySTARTTLS:
			cfg.Security = notify.SMTPSecuritySTARTTLS
			if cfg.Port == 0 {
				cfg.Port = 587
			}
		case notify.SMTPSecurityTLS:
			if cfg.Port == 0 {
				cfg.Port = 465
			}
		case notify.SMTPSecurityNone:
			if cfg.Port == 0 {
				cfg.Port = 25
			}
		default:
			return nil, fmt.Errorf("email sink unsupported security %q", cfg.Security)
		}
		if cfg.UseAuth && cfg.Username == "" {
			return nil, fmt.Errorf("email sink requires username when authentication is enabled")
		}
		return &emailSink{cfg: cfg}, nil
	})
}

// Deliver renders and sends the email within ctx.
func (s *emailSink) Deliver(ctx context.Context, env Envelope) Outcome {
	payload, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return Outcome{Result: ResultTerminal, Reason: "encode envelope: " + err.Error()}
	}
	subject := s.cfg.Subject
	if subject == "" {
		subject = "NereidLink forwarded data"
		if env.DeviceID != "" {
			subject = "NereidLink data from " + env.DeviceID
		}
	}
	msg := notify.Message{
		Subject: subject,
		Body:    string(payload),
	}
	username, password := "", ""
	if s.cfg.UseAuth {
		username, password = s.cfg.Username, s.cfg.Password
	}
	if err := notify.DeliverEmailContext(ctx, notify.SMTPConfig{
		Host:      s.cfg.Host,
		Port:      s.cfg.Port,
		From:      s.cfg.From,
		LocalName: s.cfg.EHLODomain,
		Security:  s.cfg.Security,
		Username:  username,
		Password:  password,
	}, s.cfg.To, msg); err != nil {
		// Fail toward retry, never a silent drop; the worker parks the
		// message after max_attempts.
		return Outcome{Result: ResultRetryable, Reason: err.Error()}
	}
	return Outcome{Result: ResultDelivered}
}

// Compile-time guard.
var _ Sink = (*emailSink)(nil)
