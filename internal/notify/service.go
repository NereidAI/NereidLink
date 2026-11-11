// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go runs the notification event loop with frequency control and
// the CRITICAL/normal delivery pools.

package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/nereidai/nereidlink/internal/alarm"
)

// queueGroup makes alarm consumption load balanced across notifier
// replicas; the MVP runs one instance.
const queueGroup = "nl-notifier"

// Service consumes alarms and delivers notifications.
type Service struct {
	nc  *nats.Conn
	cfg Config

	// recipients lists email targets for alarm mail.
	recipients []string

	// inbox records in-app notifications for WS push.
	inbox *Inbox

	// metrics instruments delivery; nil-safe until registered.
	metrics *deliveryMetrics

	mu       sync.Mutex
	lastSent map[string]time.Time

	criticalCh chan Message
	normalCh   chan Message
}

// NewService constructs the notification service.
func NewService(nc *nats.Conn, cfg Config, emailRecipients []string, inbox *Inbox) *Service {
	return &Service{
		nc:         nc,
		cfg:        cfg,
		recipients: emailRecipients,
		inbox:      inbox,
		metrics:    newDeliveryMetrics(),
		lastSent:   map[string]time.Time{},
		criticalCh: make(chan Message, 1024),
		normalCh:   make(chan Message, 1024),
	}
}

// RegisterMetrics publishes the delivery instruments onto reg.
func (s *Service) RegisterMetrics(reg prometheus.Registerer) error {
	return s.metrics.register(reg)
}

// QueueWatermarks reports the bounded delivery-queue occupancy of each pool
// as current/cap, backing the ADR-0025 capacity watermark metrics.
func (s *Service) QueueWatermarks() (critical [2]int, normal [2]int) {
	return [2]int{len(s.criticalCh), cap(s.criticalCh)},
		[2]int{len(s.normalCh), cap(s.normalCh)}
}

// Run subscribes to alarm events and starts both delivery pools.
func (s *Service) Run(ctx context.Context) error {
	sub, err := s.nc.QueueSubscribe(alarm.Wildcard, queueGroup,
		func(msg *nats.Msg) { s.handleEvent(msg.Data) })
	if err != nil {
		return fmt.Errorf("subscribe alarms: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	var wg sync.WaitGroup
	for i := 0; i < s.cfg.CriticalWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.worker(ctx, s.criticalCh)
		}()
	}
	for i := 0; i < s.cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.worker(ctx, s.normalCh)
		}()
	}

	<-ctx.Done()
	wg.Wait()
	return nil
}

// handleEvent applies frequency control and queues rendered messages.
func (s *Service) handleEvent(raw []byte) {
	var event alarmEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		slog.Warn("notify bad alarm event", "error", err)
		return
	}

	if !s.shouldNotify(event) {
		return
	}
	for _, msg := range s.buildMessages(event) {
		s.queue(msg)
	}
	s.recordInApp(event)
}

// recordInApp records one in-app notification from the rendered English
// subject/body. Failures are logged but never block channel delivery.
func (s *Service) recordInApp(event alarmEvent) {
	if s.inbox == nil {
		return
	}
	subject, body := renderTemplate(LanguageEnglish, EventTrigger, event)
	if event.State == "CLEARED" {
		subject, body = renderTemplate(LanguageEnglish, EventClear, event)
	}
	if _, err := s.inbox.Record(context.Background(), RecordInput{
		TenantID: event.TenantID,
		AlarmID:  event.AlarmID,
		DeviceID: event.DeviceID,
		Severity: event.Severity,
		Title:    subject,
		Body:     body,
	}); err != nil {
		slog.Warn("record in-app notification failed", "error", err)
	}
}

// shouldNotify implements ADR-0019 frequency rules.
func (s *Service) shouldNotify(event alarmEvent) bool {
	key := event.TenantID + "|" + event.DedupeKey

	if event.State == "CLEARED" {
		// CRITICAL/MAJOR recovery is always notified; lower severities
		// only update state.
		return event.Severity == "CRITICAL" || event.Severity == "MAJOR"
	}

	if event.Created || event.Escalated {
		s.remember(key)
		return true
	}

	if time.Since(s.saw(key)) >= s.cfg.FrequencyWindow {
		s.remember(key)
		return true
	}
	return false
}

// buildMessages renders one message per enabled channel.
func (s *Service) buildMessages(event alarmEvent) []Message {
	kind := EventTrigger
	if event.State == "CLEARED" {
		kind = EventClear
	}
	subject, body := renderTemplate(LanguageEnglish, kind, event)

	// Parse the alarm start for the end-to-end latency histogram; a
	// malformed/empty timestamp leaves it zero and is simply skipped.
	var startedAt time.Time
	if event.StartedAt != "" {
		if parsed, err := time.Parse(time.RFC3339, event.StartedAt); err == nil {
			startedAt = parsed
		}
	}

	messages := []Message{}
	if s.cfg.WebhookURL != "" {
		messages = append(messages, Message{
			Channel:        KindWebhook,
			Target:         s.cfg.WebhookURL,
			Subject:        subject,
			Body:           body,
			Severity:       event.Severity,
			AlarmStartedAt: startedAt,
		})
	}
	if s.cfg.SMTP.Enabled {
		messages = append(messages, Message{
			Channel:        KindSMTP,
			Target:         "",
			Subject:        subject,
			Body:           body,
			Severity:       event.Severity,
			AlarmStartedAt: startedAt,
		})
	}
	return messages
}

// queue routes one message into the severity-specific pool.
func (s *Service) queue(msg Message) {
	if msg.Severity == "CRITICAL" {
		s.criticalCh <- msg
	} else {
		s.normalCh <- msg
	}
}

// worker drains one delivery pool and invokes the configured channels.
func (s *Service) worker(ctx context.Context, ch <-chan Message) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			s.deliver(ctx, msg)
		}
	}
}

// deliver executes one channel call with the configured timeout and
// bounded retry of a failed webhook, recording delivery metrics.
func (s *Service) deliver(ctx context.Context, msg Message) {
	callCtx, cancel := context.WithTimeout(ctx, s.cfg.DeliveryTimeout)
	defer cancel()

	callStart := time.Now()
	var err error
	switch msg.Channel {
	case KindWebhook:
		err = deliverWebhook(callCtx, msg.Target, msg)
	case KindSMTP:
		err = deliverEmailContext(callCtx, s.cfg.SMTP, s.recipients, msg)
	default:
		return
	}
	duration := time.Since(callStart)
	if s.metrics != nil {
		s.metrics.observe(msg, duration, err != nil)
	}
	if err != nil {
		slog.Warn("notification delivery failed",
			"channel", msg.Channel, "severity", msg.Severity, "error", err)
	}
}

// remember records the last notification time for a dedupe key.
func (s *Service) remember(key string) {
	s.mu.Lock()
	s.lastSent[key] = time.Now()
	s.mu.Unlock()
}

// saw returns the last notification time.
func (s *Service) saw(key string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSent[key]
}
