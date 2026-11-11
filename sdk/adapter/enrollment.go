// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Enrollment is the module identity returned on registration.
type Enrollment struct {
	// ID is the module identifier used on heartbeat requests.
	ID string `json:"id"`

	// Type is the provisioned protocol family.
	Type string `json:"type"`

	// IngressID is the dispatch subject of the module.
	IngressID string `json:"ingress_id"`

	// Status is ONLINE after a successful registration.
	Status string `json:"status"`

	// Version is the registered adapter build version.
	Version string `json:"version"`
}

// restEnvelope is the standard management error envelope.
type restEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Enroll registers the adapter with the core and returns its enrollment.
// The provisioned module type and ingressID must match the token; a
// non-2xx response, including a type mismatch, is returned as an error.
func Enroll(ctx context.Context, baseURL, token, typ, ingressID, version string) (Enrollment, error) {
	body := map[string]string{
		"enrollment_token": token,
		"type":             typ,
		"ingress_id":       ingressID,
		"version":          version,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Enrollment{}, fmt.Errorf("marshal registration: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/v1/modules/register", bytes.NewReader(raw))
	if err != nil {
		return Enrollment{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Enrollment{}, fmt.Errorf("register module: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Enrollment{}, decodeError(resp)
	}
	var enrolled Enrollment
	if err := json.NewDecoder(resp.Body).Decode(&enrolled); err != nil {
		return Enrollment{}, fmt.Errorf("decode registration: %w", err)
	}
	return enrolled, nil
}

// Heartbeat sends one lease renewal.
func Heartbeat(ctx context.Context, baseURL, id, token string) error {
	raw, err := json.Marshal(map[string]string{
		"enrollment_token": token,
	})
	if err != nil {
		return fmt.Errorf("marshal heartbeat: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/v1/modules/"+id+"/heartbeat", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("send heartbeat: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return decodeError(resp)
	}
	return nil
}

// RunHeartbeats renews the lease on every interval until ctx is canceled.
func RunHeartbeats(ctx context.Context, baseURL, id, token string, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := Heartbeat(ctx, baseURL, id, token); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// decodeError extracts the management error code and message.
func decodeError(resp *http.Response) error {
	var envelope restEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("management request failed with status %d", resp.StatusCode)
	}
	return fmt.Errorf("%s: %s", envelope.Error.Code, envelope.Error.Message)
}
