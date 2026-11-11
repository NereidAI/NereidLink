// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package companion

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CompanionClient is the transport seam. Production uses HTTPClient; tests
// substitute a fake so owner reconcile logic needs no network.
type CompanionClient interface {
	// CheckContract verifies the installation advertises the endpoint's
	// contract version through GET /api/v1/version.
	CheckContract(ctx context.Context) error
	// PutSubscriptions performs the idempotent full desired-set replacement
	// and returns the per-row read-back.
	PutSubscriptions(ctx context.Context, rows []DesiredRow) ([]ReadbackRow, error)
}

// HTTPClient talks one companion-subscription/v1 installation. One client
// is constructed per registered endpoint; it carries no per-request state.
type HTTPClient struct {
	endpoint Endpoint
	http     *http.Client
}

// NewHTTPClient builds the contract client for one endpoint.
func NewHTTPClient(ep Endpoint) *HTTPClient {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if ep.InsecureSkipVerify {
		//nolint:gosec // G402: explicit operator opt-out for lab endpoints.
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &HTTPClient{
		endpoint: ep,
		http:     &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}
}

// CheckContract implements CompanionClient.
func (c *HTTPClient) CheckContract(ctx context.Context) error {
	var version wireVersion
	if err := c.do(ctx, http.MethodGet, versionPath, nil, &version); err != nil {
		return err
	}
	if version.APIVersion != c.endpoint.ContractVersion {
		return fmt.Errorf("%w: endpoint %s advertises %q, want %q",
			ErrContractMismatch, c.endpoint.BaseURL, version.APIVersion, c.endpoint.ContractVersion)
	}
	return nil
}

// PutSubscriptions implements CompanionClient.
func (c *HTTPClient) PutSubscriptions(ctx context.Context, rows []DesiredRow) ([]ReadbackRow, error) {
	wireRows := make([]wireSubscription, 0, len(rows))
	for _, row := range rows {
		camera := row.Camera
		if camera == "" {
			// The reference companion defaults a blank camera label to the
			// source device; send it explicitly so every conformant service
			// behaves identically without local defaults.
			camera = row.SourceDevice
		}
		wireRows = append(wireRows, wireSubscription{
			ID:                 row.RowID,
			SourceDevice:       row.SourceDevice,
			Camera:             camera,
			PipelineID:         row.PipelineID,
			ConclusionDeviceID: row.ConclusionDeviceID,
			Enabled:            row.Enabled,
		})
	}
	var response wirePutResponse
	if err := c.do(ctx, http.MethodPut, subsPath, wirePutRequest{Subscriptions: wireRows}, &response); err != nil {
		return nil, err
	}
	readback := make([]ReadbackRow, 0, len(response.Items))
	for _, item := range response.Items {
		readback = append(readback, ReadbackRow(item))
	}
	return readback, nil
}

// do executes one JSON exchange. Non-2xx responses become typed transport
// errors; the companion's reason body is preserved for deployment issues.
func (c *HTTPClient) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode companion request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.endpoint.BaseURL, "/")+path, reader)
	if err != nil {
		return fmt.Errorf("build companion request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set(headerContract, c.endpoint.ContractVersion)
	if c.endpoint.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.endpoint.AuthToken)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("companion request %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read companion response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: companion %s %s returned %d: %s",
			ErrUpstreamError, method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode companion response: %w", err)
	}
	return nil
}
