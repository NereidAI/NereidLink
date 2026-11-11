// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// manager_test.go covers the pure request validation rules that do not need a
// database or broker.

package probe

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestValidateRequest(t *testing.T) {
	tune := DefaultTuning()
	good := uuid.NewString()

	cases := []struct {
		name string
		req  Request
		want error
	}{
		{"ok default mode", Request{Devices: []string{good}}, nil},
		{"ok cached", Request{Devices: []string{good}, Mode: ModeCached}, nil},
		{"ok strict", Request{Devices: []string{good}, Mode: ModeStrict}, nil},
		{"bad mode", Request{Devices: []string{good}, Mode: "bogus"}, ErrInvalidArgument},
		{"empty devices", Request{Mode: ModeFresh}, ErrInvalidArgument},
		{"bad uuid", Request{Devices: []string{"nope"}}, ErrInvalidArgument},
		{"negative timeout", Request{Devices: []string{good}, TimeoutMS: -1}, ErrInvalidArgument},
		{"too many", Request{Devices: manyIDs(tune.MaxDevices + 1)}, ErrTooManyDevices},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRequest(tc.req, tune)
			if tc.want == nil && err != nil {
				t.Fatalf("want ok, got %v", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestDedupe(t *testing.T) {
	out := dedupe([]string{"a", "b", "a", "c", "b"})
	if len(out) != 3 || out[0] != "a" || out[1] != "b" || out[2] != "c" {
		t.Fatalf("unexpected dedupe: %v", out)
	}
}

func manyIDs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = uuid.NewString()
	}
	return out
}
