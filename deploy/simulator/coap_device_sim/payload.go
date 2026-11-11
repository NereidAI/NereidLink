// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// payloadSource renders custom telemetry frames. It mirrors the Python
// simulators: a JSON object repeats every frame and a JSON array cycles
// element by element; template tokens ${i}, ${ts} and ${uuid} are replaced
// in the raw JSON text. A nil source selects the sine-wave default frame.
type payloadSource struct {
	// templates holds compact JSON text, one entry per cycled frame.
	templates []string
}

// newPayloadSource builds a source from an inline payload or payload file.
// Both empty yields a nil source. The first rendered frame is validated so
// malformed templates fail at startup.
func newPayloadSource(payload, payloadFile string) (*payloadSource, error) {
	if payload != "" && payloadFile != "" {
		return nil, fmt.Errorf("--payload and --payload-file are mutually exclusive")
	}
	if payload == "" && payloadFile == "" {
		return nil, nil
	}

	raw := []byte(payload)
	if payloadFile != "" {
		// The file path is an explicit operator CLI argument, not external
		// input; gosec G304 is a false positive here.
		data, err := os.ReadFile(payloadFile) //nolint:gosec
		if err != nil {
			return nil, fmt.Errorf("read payload file: %w", err)
		}
		raw = data
	}

	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("parse payload: %w", err)
	}

	source := &payloadSource{}
	switch data := value.(type) {
	case map[string]any:
		compact, err := compactJSON(data)
		if err != nil {
			return nil, err
		}
		source.templates = []string{compact}
	case []any:
		if len(data) == 0 {
			return nil, fmt.Errorf("payload array must not be empty")
		}
		for _, item := range data {
			if _, ok := item.(map[string]any); !ok {
				return nil, fmt.Errorf("payload array entries must be objects")
			}
			compact, err := compactJSON(item)
			if err != nil {
				return nil, err
			}
			source.templates = append(source.templates, compact)
		}
	default:
		return nil, fmt.Errorf("payload must be a JSON object or array")
	}

	frame, err := source.render(0)
	if err != nil {
		return nil, err
	}
	if !json.Valid([]byte(frame)) {
		return nil, fmt.Errorf("rendered frame is not valid JSON")
	}
	return source, nil
}

// render returns the frame text for index, applying template substitution.
func (s *payloadSource) render(index int) (string, error) {
	template := s.templates[index%len(s.templates)]
	replacer := strings.NewReplacer(
		"${i}", strconv.Itoa(index),
		"${ts}", strconv.FormatInt(time.Now().UnixMilli(), 10),
		"${uuid}", uuid.NewString(),
	)
	return replacer.Replace(template), nil
}

// compactJSON marshals a value without extra whitespace.
func compactJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode payload entry: %w", err)
	}
	return string(raw), nil
}
