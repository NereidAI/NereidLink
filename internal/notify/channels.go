// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// channels.go implements the built-in webhook and SMTP delivery channels.

package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
)

// DeliverWebhook is the exported webhook channel entry used by adapters
// outside the package.
func DeliverWebhook(ctx context.Context, url string, msg Message) error {
	return deliverWebhook(ctx, url, msg)
}

// deliverWebhook posts one JSON notification to the webhook URL.
func deliverWebhook(ctx context.Context, url string, msg Message) error {
	payload, err := json.Marshal(map[string]string{
		"subject":  msg.Subject,
		"body":     msg.Body,
		"severity": msg.Severity,
	})
	if err != nil {
		return fmt.Errorf("webhook encode: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("webhook send: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", response.StatusCode)
	}
	return nil
}

// SMTP transport security modes.
const (
	// SMTPSecuritySTARTTLS upgrades a plain connection after EHLO.
	SMTPSecuritySTARTTLS = "starttls"
	// SMTPSecurityTLS dials an implicit-TLS connection (SMTPS).
	SMTPSecurityTLS = "tls"
	// SMTPSecurityNone sends over a plaintext connection.
	SMTPSecurityNone = "none"
)

// normalizeSecurity resolves the configured mode, defaulting to STARTTLS.
func normalizeSecurity(mode string) string {
	switch mode {
	case SMTPSecurityTLS, SMTPSecurityNone:
		return mode
	default:
		return SMTPSecuritySTARTTLS
	}
}

// DeliverEmail is the exported SMTP channel entry used by adapters
// outside the package.
func DeliverEmail(cfg SMTPConfig, recipients []string, msg Message) error {
	// The non-context call site historically used SendMail; route it
	// through the same implementation so implicit TLS, EHLO identity and
	// the security switch behave identically. The dial/handshake carry
	// their own connect timeout.
	return deliverEmailContext(context.Background(), cfg, recipients, msg)
}

// DeliverEmailContext is the context-aware SMTP entry. Unlike DeliverEmail it
// dials and runs the protocol under ctx (pushing the ctx deadline onto the
// connection), so a slow or unreachable SMTP server is bounded by the
// caller's deadline. It negotiates STARTTLS when the server advertises it.
func DeliverEmailContext(
	ctx context.Context,
	cfg SMTPConfig,
	recipients []string,
	msg Message,
) error {
	return deliverEmailContext(ctx, cfg, recipients, msg)
}

// emailContent renders the RFC 822 message bytes shared by both send paths.
func emailContent(cfg SMTPConfig, recipients []string, msg Message) []byte {
	return []byte(
		"To: " + joinRecipients(recipients) + "\r\n" +
			"From: " + cfg.From + "\r\n" +
			"Subject: " + msg.Subject + "\r\n" +
			"MIME-Version: 1.0\r\n" +
			"Content-Type: text/plain; charset=UTF-8\r\n\r\n" +
			msg.Body)
}

// deliverEmailContext sends one notification through SMTP entirely under ctx.
func deliverEmailContext(
	ctx context.Context,
	cfg SMTPConfig,
	recipients []string,
	msg Message,
) error {
	if len(recipients) == 0 {
		return nil
	}
	security := normalizeSecurity(cfg.Security)
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	// Establish the connection within the caller's deadline. Implicit TLS
	// (SMTPS, typically port 465) performs the handshake on the socket
	// before reading the SMTP banner; STARTTLS/none start with plain TCP.
	dialer := &net.Dialer{}
	// Bound the TCP (and TLS) handshake by the caller's deadline.
	if deadline, ok := ctx.Deadline(); ok {
		dialer.Deadline = deadline
	}
	var conn net.Conn
	if security == SMTPSecurityTLS {
		tlsConn, err := tls.DialWithDialer(dialer, "tcp", addr,
			&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
		if err != nil {
			return fmt.Errorf("smtp tls dial: %w", err)
		}
		conn = tlsConn
	} else {
		netConn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("smtp dial: %w", err)
		}
		conn = netConn
	}
	// Bound all subsequent protocol I/O by the same deadline. Without this,
	// a server that connects but then stalls mid-handshake could outlive ctx.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp handshake: %w", err)
	}
	defer func() { _ = client.Close() }()

	// Announce a proper FQDN in EHLO/HELO. The net/smtp default
	// "localhost" is rejected by strict receivers ("Helo command
	// rejected: ACCESS DENIED"). Must run before StartTLS/Auth; STARTTLS
	// re-runs EHLO with the same name on the upgraded connection.
	if err := client.Hello(resolveLocalName(cfg)); err != nil {
		return fmt.Errorf("smtp helo: %w", err)
	}

	// STARTTLS upgrades only in starttls mode and when offered; "none"
	// stays plaintext; implicit TLS is already encrypted at this point.
	if security == SMTPSecuritySTARTTLS {
		if advertised, _ := client.Extension("STARTTLS"); advertised {
			if err := client.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
				return fmt.Errorf("smtp starttls: %w", err)
			}
		}
	}
	// Authenticate only when credentials are supplied. The forwarder form
	// gates these fields behind an explicit "use authentication" option, so
	// an open relay with AUTH disabled does not fail with 503
	// "authentication not enabled", and credentials are never sent where the
	// operator chose anonymous delivery. When AUTH is requested but the
	// server never advertises it (typically port 25, the inbound MX port),
	// return a clear error instead of the server's opaque 503.
	if cfg.Username != "" {
		if authAdvertised, mechanisms := client.Extension("AUTH"); !authAdvertised {
			return fmt.Errorf("smtp auth: server does not advertise AUTH on port %d; use the submission port (typically 587 with STARTTLS or 465 with TLS)", cfg.Port)
		} else if mechanisms == "" {
			return fmt.Errorf("smtp auth: server advertised an empty AUTH extension")
		}
		if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := client.Mail(cfg.From); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("smtp rcpt: %w", err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := writer.Write(emailContent(cfg, recipients, msg)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("smtp data close: %w", err)
	}
	return nil
}

// joinRecipients renders the To header value.
func joinRecipients(recipients []string) string {
	out := ""
	for index, addr := range recipients {
		if index > 0 {
			out += ", "
		}
		out += addr
	}
	return out
}

// resolveLocalName picks the EHLO/HELO identity: the explicit LocalName,
// else the domain part of the From address, else the configured SMTP host.
// net/smtp rejects an identity containing whitespace/control characters.
func resolveLocalName(cfg SMTPConfig) string {
	clean := func(value string) string {
		value = strings.TrimSpace(value)
		for _, bad := range []string{"\r", "\n", " "} {
			value = strings.ReplaceAll(value, bad, "")
		}
		return value
	}
	if name := clean(cfg.LocalName); name != "" {
		return name
	}
	if at := strings.LastIndex(cfg.From, "@"); at >= 0 {
		if domain := clean(cfg.From[at+1:]); domain != "" {
			return domain
		}
	}
	if host := clean(cfg.Host); host != "" {
		return host
	}
	return "localhost"
}
