// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// json.go provides the small JSON response helpers used by device-facing
// endpoints. The error document shape matches the management REST error
// envelope so clients handle both surfaces uniformly.

package ingesthttp

import (
	"encoding/json"
	"net/http"
)

// deviceErrorBody is the JSON error envelope.
type deviceErrorBody struct {
	// Error carries the machine-readable code and human-readable message.
	Error deviceErrorDetail `json:"error"`
}

// deviceErrorDetail is one error.
type deviceErrorDetail struct {
	// Code identifies the failure class.
	Code string `json:"code"`

	// Message describes the failure.
	Message string `json:"message"`
}

// writeDeviceJSON encodes value with the given status.
func writeDeviceJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// writeDeviceError encodes one JSON error.
func writeDeviceError(w http.ResponseWriter, status int, code, message string) {
	writeDeviceJSON(w, status, deviceErrorBody{
		Error: deviceErrorDetail{Code: code, Message: message},
	})
}
