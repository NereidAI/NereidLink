// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package horizon

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ErrUpstream wraps every non-2xx NH response. Status lets callers map NH's
// error vocabulary (409 precision not ready, 502 transport failure, ...)
// onto deployment errors without parsing message text.
var ErrUpstream = errors.New("nereidhorizon upstream error")

// UpstreamError carries the HTTP status and trimmed NH error body.
type UpstreamError struct {
	Status int
	Body   string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("%v: NH returned %d: %s", ErrUpstream, e.Status, e.Body)
}

func (e *UpstreamError) Unwrap() error { return ErrUpstream }

// TaskClient is the transport seam. Production uses HTTPClient; the business
// owner tests substitute an in-memory fake so no network is required.
type TaskClient interface {
	// EnsureTask posts the idempotent ensure envelope. 200 is an idempotent
	// replay, 201 a fresh publication or an adopted ESS object.
	EnsureTask(ctx context.Context, req EnsureRequest) (Task, error)
	// GetTask polls one task handle.
	GetTask(ctx context.Context, taskID string) (Task, error)
}

// HTTPClient talks one NereidHorizon installation under one org-bound API
// key. It carries no per-request state.
type HTTPClient struct {
	baseURL string
	org     string
	apiKey  string
	http    *http.Client
}

// ClientConfig carries the transport-relevant HorizonSection fields.
type ClientConfig struct {
	BaseURL            string
	Org                string
	APIKey             string
	CAFile             string
	InsecureSkipVerify bool
}

// NewHTTPClient builds the contract client. A custom CA or an explicit
// lab-only skip-verify configures the TLS transport; the default uses the
// system roots.
func NewHTTPClient(cfg ClientConfig) (*HTTPClient, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12} //nolint:gosec // G402: min TLS 1.2 for an internal service contract
	if cfg.InsecureSkipVerify {
		//nolint:gosec // G402: explicit operator opt-out for lab endpoints.
		tlsConfig.InsecureSkipVerify = true
	}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("load horizon CA bundle: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("horizon CA bundle %s contains no PEM certificate", cfg.CAFile)
		}
		tlsConfig.RootCAs = pool
	}
	transport.TLSClientConfig = tlsConfig
	return &HTTPClient{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		org:     cfg.Org,
		apiKey:  cfg.APIKey,
		http:    &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}, nil
}

// EnsureTask implements TaskClient.
func (c *HTTPClient) EnsureTask(ctx context.Context, req EnsureRequest) (Task, error) {
	var task Task
	path := fmt.Sprintf("/api/service/v1/orgs/%s/distribution-tasks", c.org)
	if err := c.do(ctx, http.MethodPost, path, req, &task); err != nil {
		return Task{}, err
	}
	return task, nil
}

// GetTask implements TaskClient.
func (c *HTTPClient) GetTask(ctx context.Context, taskID string) (Task, error) {
	var task Task
	path := fmt.Sprintf("/api/service/v1/orgs/%s/distribution-tasks/%s", c.org, taskID)
	if err := c.do(ctx, http.MethodGet, path, nil, &task); err != nil {
		return Task{}, err
	}
	return task, nil
}

// do executes one JSON exchange authenticated with the org-bound X-Api-Key.
// Non-2xx responses become typed UpstreamError values; NH's reason body is
// preserved for deployment diagnostics.
func (c *HTTPClient) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode horizon request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build horizon request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Api-Key", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("horizon request %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read horizon response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &UpstreamError{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode horizon response: %w", err)
	}
	return nil
}
