// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package deadman implements the ADR-0019 "dead-man" fallback channel: a
// notification path for the platform's own CRITICAL problems whose
// endpoint is configured entirely independently of the tenant-facing
// notifier. It exists so that a failure of the ordinary notification stack
// (or of a monitored dependency) can still reach an operator.
//
// A Monitor periodically runs pluggable probes (for example database
// connectivity and the NATS link). After a configurable number of
// consecutive failures it raises a CRITICAL report through its own
// webhook/SMTP channels; when the probes recover it sends the matching
// recovery report. The report path never shares clients with
// internal/notify, so a misbehaving tenant channel cannot suppress a
// platform problem.
package deadman

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

// Config configures the independent fallback channel.
type Config struct {
	// Enabled turns the monitor and its delivery on.
	Enabled bool

	// WebhookURL is the fallback webhook endpoint; empty disables webhook.
	WebhookURL string

	// SMTP is the fallback email channel; disabled unless Enabled.
	SMTP SMTPConfig

	// Interval paces probe runs.
	Interval time.Duration

	// Threshold is the number of consecutive probe failures before a
	// CRITICAL report is raised. Values below 1 are treated as 1.
	Threshold int

	// Timeout bounds one delivery call.
	Timeout time.Duration
}

// SMTPConfig configures the fallback email channel.
type SMTPConfig struct {
	// Enabled turns email delivery on.
	Enabled bool
	// Host is the SMTP server host.
	Host string
	// Port is the SMTP server port.
	Port int
	// From is the envelope and header sender.
	From string
	// Username authenticates the SMTP server when non-empty.
	Username string
	// Password authenticates the SMTP server.
	Password string
	// Recipients are the fallback alert mailboxes.
	Recipients []string
}

// Probe is one platform self-check. Returning an error marks the probe
// unhealthy.
type Probe struct {
	// Name identifies the probe in reports.
	Name string
	// Check runs the probe.
	Check func(ctx context.Context) error
}

// Notifier delivers a dead-man report over the independent channels.
type Notifier struct {
	cfg    Config
	client *http.Client
}

// NewNotifier builds a notifier with dedicated HTTP/SMTP clients.
func NewNotifier(cfg Config) *Notifier {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Notifier{cfg: cfg, client: &http.Client{Timeout: timeout}}
}

// Report delivers a platform problem labelled code. Delivery errors are
// logged by the caller; the method returns the first webhook error while
// still attempting email.
func (n *Notifier) Report(ctx context.Context, code, subject, body string) error {
	if !n.cfg.Enabled {
		return nil
	}
	severity := "CRITICAL"
	var firstErr error
	if n.cfg.WebhookURL != "" {
		if err := n.postWebhook(ctx, code, subject, body, severity); err != nil {
			firstErr = err
		}
	}
	if n.cfg.SMTP.Enabled && len(n.cfg.SMTP.Recipients) > 0 {
		if err := n.sendEmail(subject, body); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// postWebhook posts one JSON report to the fallback webhook.
func (n *Notifier) postWebhook(ctx context.Context, code, subject, body, severity string) error {
	payload, err := json.Marshal(map[string]string{
		"code":     code,
		"subject":  subject,
		"body":     body,
		"severity": severity,
		"source":   "deadman",
	})
	if err != nil {
		return fmt.Errorf("deadman encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.WebhookURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("deadman request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("deadman webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("deadman webhook returned status %d", resp.StatusCode)
	}
	return nil
}

// sendEmail sends one fallback email.
func (n *Notifier) sendEmail(subject, body string) error {
	cfg := n.cfg.SMTP
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	content := "To: " + strings.Join(cfg.Recipients, ", ") + "\r\n" +
		"From: " + cfg.From + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n" +
		body
	if err := smtp.SendMail(addr, auth, cfg.From, cfg.Recipients, []byte(content)); err != nil {
		return fmt.Errorf("deadman smtp: %w", err)
	}
	return nil
}

// Monitor runs probes and reports platform health transitions.
type Monitor struct {
	notifier  *Notifier
	probes    []Probe
	interval  time.Duration
	threshold int
	log       *slog.Logger

	mu       sync.Mutex
	failures int
	reported bool
}

// NewMonitor builds a monitor. Interval defaults to 30s and threshold to 3
// so a single transient probe failure does not page an operator.
func NewMonitor(notifier *Notifier, log *slog.Logger, probes ...Probe) *Monitor {
	return &Monitor{
		notifier:  notifier,
		probes:    probes,
		interval:  30 * time.Second,
		threshold: 3,
		log:       log,
	}
}

// WithSchedule overrides the interval and threshold.
func (m *Monitor) WithSchedule(interval time.Duration, threshold int) *Monitor {
	if interval > 0 {
		m.interval = interval
	}
	if threshold > 0 {
		m.threshold = threshold
	}
	return m
}

// Run probes on the configured interval until ctx is canceled. On the
// threshold-th consecutive failure it reports a platform problem exactly
// once; on recovery it reports the recovery exactly once.
func (m *Monitor) Run(ctx context.Context) {
	if m.notifier == nil || !m.notifier.cfg.Enabled {
		return
	}
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.runOnce(ctx)
		}
	}
}

// runOnce executes all probes and updates the reported state.
func (m *Monitor) runOnce(ctx context.Context) {
	var failed []string
	for _, p := range m.probes {
		if err := p.Check(ctx); err != nil {
			failed = append(failed, p.Name)
			m.log.Warn("deadman probe failed", "probe", p.Name, "error", err)
		}
	}

	m.mu.Lock()
	threshold := m.threshold
	if threshold < 1 {
		threshold = 1
	}
	if len(failed) > 0 {
		m.failures++
		shouldReport := m.failures >= threshold && !m.reported
		if shouldReport {
			m.reported = true
		}
		m.mu.Unlock()
		if shouldReport {
			subject := "NereidLink platform problem"
			body := fmt.Sprintf("Platform probes failing: %s", strings.Join(failed, ", "))
			if err := m.notifier.Report(ctx, "PLATFORM_PROBLEM", subject, body); err != nil {
				m.log.Error("deadman report failed", "error", err)
			}
		}
		return
	}

	recovered := m.reported
	m.failures = 0
	m.reported = false
	m.mu.Unlock()
	if recovered {
		if err := m.notifier.Report(ctx, "PLATFORM_PROBLEM_CLEARED",
			"NereidLink platform recovered", "All platform probes are healthy again."); err != nil {
			m.log.Error("deadman recovery report failed", "error", err)
		}
	}
}
