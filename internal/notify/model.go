// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package notify implements the notification framework from ADR-0019: it
// consumes alarm events, applies frequency control, renders localized
// templates and delivers through pluggable channels. The MVP ships two
// built-in channels (HTTP webhook and SMTP email); other channels run as
// out-of-process adapters added later.
//
// CRITICAL notifications use a dedicated delivery pool so they cannot be
// queued behind lower-severity traffic.
package notify

import (
	"time"
)

// Channel kinds configured for delivery.
const (
	// KindWebhook is the built-in HTTP webhook channel.
	KindWebhook = "webhook"

	// KindSMTP is the built-in SMTP email channel.
	KindSMTP = "smtp"
)

// Event kind labels used in template selection.
const (
	// EventTrigger is a new or updated alarm.
	EventTrigger = "alarm.trigger"

	// EventClear is alarm recovery.
	EventClear = "alarm.clear"
)

// Language identifiers of the built-in template sets.
const (
	// LanguageEnglish is the default template language.
	LanguageEnglish = "en"

	// LanguageChinese is the built-in Chinese template language.
	LanguageChinese = "zh"
)

// SMTPConfig configures the built-in email channel. Email is off unless
// Enabled is true.
type SMTPConfig struct {
	// Enabled turns email delivery on.
	Enabled bool

	// Host is the SMTP server host.
	Host string

	// Port is the SMTP server port.
	Port int

	// From is the envelope and header sender address.
	From string

	// LocalName is the FQDN presented in EHLO/HELO. When empty it
	// defaults to the domain part of From; servers with strict HELO
	// policies reject the library default "localhost".
	LocalName string

	// Security selects the transport encryption: "starttls" upgrades a
	// plain connection after EHLO (typically port 587), "tls" dials
	// implicit TLS (typically port 465), "none" stays unencrypted
	// (typically port 25). Empty defaults to "starttls".
	Security string

	// Username authenticates the SMTP server when non-empty.
	Username string

	// Password authenticates the SMTP server.
	Password string
}

// Config configures the notification service.
type Config struct {
	// FrequencyWindow suppresses repeated same-key updates.
	FrequencyWindow time.Duration

	// WebhookURL enables the webhook channel when non-empty.
	WebhookURL string

	// SMTP configures the email channel.
	SMTP SMTPConfig

	// CriticalWorkers bounds the dedicated CRITICAL delivery pool.
	CriticalWorkers int

	// Workers bounds the normal-severity delivery pool.
	Workers int

	// DeliveryTimeout bounds one channel call.
	DeliveryTimeout time.Duration
}

// Message is one rendered notification queued for delivery.
type Message struct {
	// Channel is the target channel kind.
	Channel string

	// Target is the channel endpoint (URL or email address).
	Target string

	// Subject is a short title (email subject, webhook metadata).
	Subject string

	// Body is the rendered text.
	Body string

	// Severity routes the message to the correct delivery pool.
	Severity string

	// AlarmStartedAt is the originating alarm start time, used for the
	// end-to-end trigger->deliver latency. Zero when unavailable.
	AlarmStartedAt time.Time
}
