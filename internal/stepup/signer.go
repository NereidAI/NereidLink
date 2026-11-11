// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// signer.go produces the signed QR payload for a challenge and the hash of
// its action details. The payload lets an out-of-band reviewer (a future
// authenticator-app feature) check which action is approved and until when;
// approval itself remains typing the 6-digit code.

package stepup

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// payloadVersion identifies the signed-content format.
const payloadVersion = "v1"

// canonicalJSON marshals a value with sorted object keys by round-tripping
// through a generic structure, so the details hash is stable across Go map
// iteration ordering.
func canonicalJSON(value map[string]any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	collapsed := collapseNumbers(value)
	raw, err := json.Marshal(collapsed)
	if err != nil {
		return nil, fmt.Errorf("canonicalize step-up details: %w", err)
	}
	return raw, nil
}

// collapseNumbers converts float64 (json's default number type) to the most
// compact integral representation where exact, avoiding 1 vs 1.0 divergence.
func collapseNumbers(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = collapseNumbers(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = collapseNumbers(item)
		}
		return out
	case float64:
		if typed == float64(int64(typed)) {
			return int64(typed)
		}
		return typed
	default:
		return value
	}
}

// hashDetails returns the hex sha256 of the canonical action details.
func hashDetails(details map[string]any) (string, error) {
	raw, err := canonicalJSON(details)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// signingInput is the exact byte string the signature covers. Fields are
// line-separated and fixed in order; changing this invalidates old payloads.
func signingInput(action, detailsSHA string, expiresAt time.Time) []byte {
	return []byte(payloadVersion + "\n" + action + "\n" + detailsSHA + "\n" +
		fmt.Sprint(expiresAt.Unix()))
}

// signPayload returns the nl-stepup signed string for QR rendering.
func signPayload(key []byte, action, detailsSHA string, expiresAt time.Time) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(signingInput(action, detailsSHA, expiresAt))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	query := url.Values{}
	query.Set("a", action)
	query.Set("d", detailsSHA)
	query.Set("exp", fmt.Sprint(expiresAt.Unix()))
	query.Set("sig", sig)
	return "nl-stepup://" + payloadVersion + "?" + query.Encode()
}
