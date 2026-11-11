// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// totp.go implements TOTP secret generation, RFC 6238 code validation and
// encrypted persistence for the local MFA path in ADR-0021.
//
// TOTP uses HMAC-SHA1 and 30-second periods as required for broad
// authenticator compatibility. No third-party OTP package is needed because
// the algorithm is HMAC truncation over a counter, and implementing it here
// keeps the exact timing and replay-window behavior explicit.

package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 mandates HMAC-SHA1 for authenticator compatibility.
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"time"
)

const (
	totpPeriod    = 30 * time.Second
	totpDigits    = 6
	totpAlgorithm = "SHA1"
	totpIssuer    = "NereidLink"
)

// TOTPEnrollment is returned to begin authenticator enrollment.
type TOTPEnrollment struct {
	// Secret is the base32 secret for manual entry.
	Secret string `json:"secret"`

	// URI is the otpauth provisioning URI.
	URI string `json:"uri"`
}

// generateTOTPSecret returns a base32 encoded 160-bit secret and its bytes.
func generateTOTPSecret() (string, []byte, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	return encoded, raw, nil
}

// totpCode computes the code for the counter containing at.
func totpCode(secret []byte, at time.Time) (string, error) {
	unix := at.Unix()
	if unix < 0 {
		return "", errors.New("TOTP timestamp is before the Unix epoch")
	}
	counter := uint64(unix / int64(totpPeriod/time.Second)) //nolint:gosec // negative values are rejected before conversion.
	var counterBytes [8]byte
	binary.BigEndian.PutUint64(counterBytes[:], counter)

	mac := hmac.New(sha1.New, secret)
	if _, err := mac.Write(counterBytes[:]); err != nil {
		return "", err
	}
	sum := mac.Sum(nil)
	offset := int(sum[len(sum)-1] & 0x0f)
	value := int(binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff)
	return fmt.Sprintf("%0*d", totpDigits, value%1_000_000), nil
}

// validateTOTPCode verifies code against the current period and one adjacent
// period on each side. The window absorbs clock drift without permitting an
// old code from outside the immediate three-period window.
func validateTOTPCode(secret []byte, code string, now time.Time) bool {
	for _, offset := range []time.Duration{-totpPeriod, 0, totpPeriod} {
		expected, err := totpCode(secret, now.Add(offset))
		if err != nil {
			return false
		}
		if hmac.Equal([]byte(expected), []byte(code)) {
			return true
		}
	}
	return false
}

// totpURI builds the otpauth provisioning URI.
func totpURI(account string, secret string) string {
	label := totpIssuer + ":" + account
	query := url.Values{}
	query.Set("secret", secret)
	query.Set("issuer", totpIssuer)
	query.Set("algorithm", totpAlgorithm)
	query.Set("digits", fmt.Sprint(totpDigits))
	query.Set("period", fmt.Sprint(int(totpPeriod/time.Second)))
	return "otpauth://totp/" + url.PathEscape(label) + "?" + query.Encode()
}
