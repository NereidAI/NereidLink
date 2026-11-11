// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// sms_sink.go is the built-in generic SMS sink. SMS gateways expose different
// HTTP JSON shapes, so rather than special-case one vendor this sink posts a
// JSON body whose two field names are configurable (the recipient number and
// the text). The response is classified like the HTTP sink so the provider
// status code is recorded in the delivery log. It sends a short text summary
// of the envelope, not the full payload, which fits an SMS.

package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// smsConfig is the generic SMS provider configuration.
type smsConfig struct {
	// URL is the provider endpoint.
	URL string `json:"url"`
	// To is the recipient phone number.
	To string `json:"to"`
	// NumberField is the JSON field carrying the recipient; default "to".
	NumberField string `json:"number_field"`
	// MessageField is the JSON field carrying the text; default "text".
	MessageField string `json:"message_field"`
	// Headers are added to the request.
	Headers map[string]string `json:"headers"`
}

// smsSink delivers short text summaries through an HTTP SMS provider.
type smsSink struct {
	cfg    smsConfig
	client *http.Client
}

func init() {
	Register(SinkSMS, func(raw map[string]any) (Sink, error) {
		buf, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var cfg smsConfig
		if err := json.Unmarshal(buf, &cfg); err != nil {
			return nil, err
		}
		if cfg.URL == "" {
			return nil, fmt.Errorf("sms sink requires url")
		}
		if cfg.To == "" {
			return nil, fmt.Errorf("sms sink requires recipient")
		}
		if cfg.NumberField == "" {
			cfg.NumberField = "to"
		}
		if cfg.MessageField == "" {
			cfg.MessageField = "text"
		}
		// No client timeout; ctx sets the hard deadline.
		return &smsSink{cfg: cfg, client: &http.Client{}}, nil
	})
}

// Deliver posts the provider-shaped JSON body and classifies the response.
func (s *smsSink) Deliver(ctx context.Context, env Envelope) Outcome {
	text := smsText(env)
	body, err := json.Marshal(map[string]string{
		s.cfg.NumberField:  s.cfg.To,
		s.cfg.MessageField: text,
	})
	if err != nil {
		return Outcome{Result: ResultTerminal, Reason: "encode sms: " + err.Error()}
	}
	//nolint:gosec // G704: the SMS provider URL is an authenticated operator-configured sink.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return Outcome{Result: ResultTerminal, Reason: "build request: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range s.cfg.Headers {
		req.Header.Set(key, value)
	}

	//nolint:gosec // G704: the request targets the authenticated operator-configured SMS provider.
	resp, err := s.client.Do(req)
	if err != nil {
		return Outcome{Result: ResultRetryable, Reason: "sms request: " + err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return Outcome{Result: ResultDelivered, Code: resp.StatusCode}
	case resp.StatusCode == http.StatusRequestTimeout ||
		resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode >= 500:
		return Outcome{
			Result: ResultRetryable,
			Code:   resp.StatusCode,
			Reason: fmt.Sprintf("sms status %d", resp.StatusCode),
		}
	default:
		return Outcome{
			Result: ResultTerminal,
			Code:   resp.StatusCode,
			Reason: fmt.Sprintf("sms status %d", resp.StatusCode),
		}
	}
}

// smsText builds the short message body, prefixed with the originating device
// so the recipient can identify the source.
func smsText(env Envelope) string {
	if env.DeviceID != "" {
		return "NL " + env.DeviceID + ": new device data"
	}
	return "NereidLink: new device data"
}

// Compile-time guard.
var _ Sink = (*smsSink)(nil)
