// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// email_sink_test.go verifies email sink configuration validation and the
// port/security defaults.

package egress

import "testing"

// newEmailSink builds an email sink from the given raw config.
func newEmailSink(t *testing.T, raw map[string]any) *emailSink {
	t.Helper()
	sink, err := buildSink(Forwarder{
		SinkType:   SinkEmail,
		SinkConfig: raw,
	})
	if err != nil {
		t.Fatalf("build sink: %v", err)
	}
	return sink.(*emailSink)
}

// TestEmailSinkSecurityDefaults checks the port defaulted from each
// transport mode and the normalized security identifier.
func TestEmailSinkSecurityDefaults(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"host": "mail.example.com",
			"from": "nl@example.com",
			"to":   []string{"ops@example.com"},
		}
	}

	cases := []struct {
		security string
		port     int
	}{
		{"", 587},
		{"starttls", 587},
		{"tls", 465},
		{"none", 25},
	}
	for _, tc := range cases {
		raw := base()
		if tc.security != "" {
			raw["security"] = tc.security
		}
		sink := newEmailSink(t, raw)
		if sink.cfg.Port != tc.port {
			t.Errorf("security=%q port = %d, want %d", tc.security, sink.cfg.Port, tc.port)
		}
		wantSecurity := tc.security
		if wantSecurity == "" {
			wantSecurity = "starttls"
		}
		if sink.cfg.Security != wantSecurity {
			t.Errorf("security = %q, want %q", sink.cfg.Security, wantSecurity)
		}
	}
}

// TestEmailSinkInvalidSecurity rejects an unknown transport mode.
func TestEmailSinkInvalidSecurity(t *testing.T) {
	_, err := buildSink(Forwarder{
		SinkType: SinkEmail,
		SinkConfig: map[string]any{
			"host":     "mail.example.com",
			"from":     "nl@example.com",
			"to":       []string{"ops@example.com"},
			"security": "ssl",
		},
	})
	if err == nil {
		t.Fatal("expected error for unsupported security")
	}
}

// TestEmailSinkAuthRequiresUsername checks the factory gate.
func TestEmailSinkAuthRequiresUsername(t *testing.T) {
	_, err := buildSink(Forwarder{
		SinkType: SinkEmail,
		SinkConfig: map[string]any{
			"host":     "mail.example.com",
			"from":     "nl@example.com",
			"to":       []string{"ops@example.com"},
			"use_auth": true,
		},
	})
	if err == nil {
		t.Fatal("expected error when use_auth is set without username")
	}
}
