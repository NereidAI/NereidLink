// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// http_sink.go is the built-in HTTP/HTTPS webhook sink. It encodes the
// envelope as JSON and classifies the response so the worker applies the
// correct retry policy without the sink owning retries itself.

package egress

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// httpConfig is the webhook sink configuration.
type httpConfig struct {
	// URL is the receiver endpoint (http:// or https://).
	URL string `json:"url"`
	// Method defaults to POST when empty.
	Method string `json:"method"`
	// Headers are added to every request.
	Headers map[string]string `json:"headers"`
	// Username/Password enable HTTP Basic authentication when non-empty.
	Username string `json:"username"`
	Password string `json:"password"`
	// ClientCert/ClientKey are PEM blocks for mutual TLS authentication.
	ClientCert string `json:"client_cert"`
	ClientKey  string `json:"client_key"`
	// CACert is an optional PEM block (or bundle) for a private/internal
	// certificate authority; the public system roots stay available.
	CACert string `json:"ca_cert"`
	// InsecureSkipVerify disables certificate verification as a last resort;
	// prefer CACert. Defaults false.
	InsecureSkipVerify bool `json:"skip_tls_verify"`
}

// httpSink delivers envelopes to one webhook.
type httpSink struct {
	cfg    httpConfig
	client *http.Client
}

func init() {
	Register(SinkHTTP, func(raw map[string]any) (Sink, error) {
		buf, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var cfg httpConfig
		if err := json.Unmarshal(buf, &cfg); err != nil {
			return nil, err
		}
		if strings.TrimSpace(cfg.URL) == "" {
			return nil, fmt.Errorf("http sink requires url")
		}
		if cfg.Method == "" {
			cfg.Method = http.MethodPost
		}
		cfg.Method = strings.ToUpper(cfg.Method)
		if !allowedHTTPMethod(cfg.Method) {
			return nil, fmt.Errorf("http sink unsupported method %q", cfg.Method)
		}

		tlsConfig, err := buildTLSConfig(cfg)
		if err != nil {
			return nil, err
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if tlsConfig != nil {
			transport.TLSClientConfig = tlsConfig
		}
		// No client-wide timeout: the worker sets a hard per-call deadline
		// through ctx, which also bounds connection and response reads.
		return &httpSink{
			cfg:    cfg,
			client: &http.Client{Transport: transport},
		}, nil
	})
}

// methodsWithBody carry the JSON envelope; GET/DELETE send no body.
var methodsWithBody = map[string]bool{
	http.MethodPost:  true,
	http.MethodPut:   true,
	http.MethodPatch: true,
}

// allowedHTTPMethod reports whether method is one the sink supports.
func allowedHTTPMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// buildTLSConfig assures the TLS configuration from private CA, mutual-TLS
// client certificate and optional verification bypass. It returns nil when no
// custom TLS settings are present, leaving the default transport behavior.
func buildTLSConfig(cfg httpConfig) (*tls.Config, error) {
	needTLS := cfg.CACert != "" || cfg.ClientCert != "" ||
		cfg.InsecureSkipVerify || strings.HasPrefix(cfg.URL, "https://")
	if !needTLS {
		return nil, nil
	}

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // G402: explicit operator opt-out for private endpoints
	}

	if cfg.CACert != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(cfg.CACert)) {
			return nil, fmt.Errorf("http sink ca_cert contains no valid PEM certificate")
		}
		tlsConfig.RootCAs = pool
	}

	if cfg.ClientCert != "" || cfg.ClientKey != "" {
		if cfg.ClientCert == "" || cfg.ClientKey == "" {
			return nil, fmt.Errorf("http sink mutual TLS requires both client_cert and client_key")
		}
		// tls.X509KeyPair parses PEM and rejects mismatched key/cert early,
		// so a bad credential fails validation rather than the first call.
		pair, err := tls.X509KeyPair([]byte(cfg.ClientCert), []byte(cfg.ClientKey))
		if err != nil {
			return nil, fmt.Errorf("http sink client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{pair}
	}
	return tlsConfig, nil
}

// Deliver sends the envelope and classifies the response. For POST/PUT/PATCH
// the envelope is the JSON body; GET/DELETE carry headers/auth but no body.
func (s *httpSink) Deliver(ctx context.Context, env Envelope) Outcome {
	var bodyReader *bytes.Reader
	if methodsWithBody[s.cfg.Method] {
		body, err := json.Marshal(env)
		if err != nil {
			return Outcome{Result: ResultTerminal, Reason: "encode envelope: " + err.Error()}
		}
		bodyReader = bytes.NewReader(body)
	}

	var req *http.Request
	var err error
	if bodyReader != nil {
		//nolint:gosec // G704: the webhook URL is an authenticated operator-configured forwarder sink.
		req, err = http.NewRequestWithContext(ctx, s.cfg.Method, s.cfg.URL, bodyReader)
	} else {
		//nolint:gosec // G704: the webhook URL is an authenticated operator-configured forwarder sink.
		req, err = http.NewRequestWithContext(ctx, s.cfg.Method, s.cfg.URL, nil)
	}
	if err != nil {
		return Outcome{Result: ResultTerminal, Reason: "build request: " + err.Error()}
	}
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range s.cfg.Headers {
		req.Header.Set(key, value)
	}
	if s.cfg.Username != "" {
		req.SetBasicAuth(s.cfg.Username, s.cfg.Password)
	}

	//nolint:gosec // G704: the request targets the authenticated operator-configured webhook.
	resp, err := s.client.Do(req)
	if err != nil {
		// Network/timeout errors are transient; ctx cancellation is treated
		// the same and the message is redelivered rather than lost.
		return Outcome{Result: ResultRetryable, Reason: "request failed: " + err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return Outcome{Result: ResultDelivered, Code: resp.StatusCode}
	case resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusForbidden:
		// Auth rejections are permanent with the current credential: retry
		// cannot help, so park the message for a credential fix.
		return Outcome{
			Result: ResultTerminal,
			Code:   resp.StatusCode,
			Reason: fmt.Sprintf("webhook status %d", resp.StatusCode),
		}
	case resp.StatusCode == http.StatusRequestTimeout ||
		resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode >= 500:
		return Outcome{
			Result: ResultRetryable,
			Code:   resp.StatusCode,
			Reason: fmt.Sprintf("webhook status %d", resp.StatusCode),
		}
	default:
		// Other 4xx are permanent rejections.
		return Outcome{
			Result: ResultTerminal,
			Code:   resp.StatusCode,
			Reason: fmt.Sprintf("webhook status %d", resp.StatusCode),
		}
	}
}

// Compile-time guard that the sink satisfies the interface.
var _ Sink = (*httpSink)(nil)
