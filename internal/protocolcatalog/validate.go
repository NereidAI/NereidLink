// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// validate.go checks an operator-submitted family document before it reaches
// the store. The kernel does not interpret any of the field content; the
// checks only enforce structural integrity the console relies on (a unique
// non-empty key, a known control kind, well-formed options/bounds).

package protocolcatalog

import (
	"fmt"
	"regexp"
	"strings"
)

// familyKeyPattern restricts the primary key to an identifier-safe lowercase
// token usable in URL paths.
var familyKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// validate checks the whole input and returns ErrInvalid wrapping the first
// problem. The family key is validated only when requireKey is true (create);
// update takes the key from the path.
func validate(in SaveInput, requireKey bool) error {
	if requireKey && !familyKeyPattern.MatchString(strings.TrimSpace(in.Family)) {
		return fmt.Errorf("%w: family must be lowercase letters/digits/dashes, got %q",
			ErrInvalid, in.Family)
	}
	if strings.TrimSpace(in.DisplayName) == "" {
		return fmt.Errorf("%w: display_name is required", ErrInvalid)
	}
	connKeys := map[string]bool{}
	for i, f := range in.ConnectionFields {
		if err := validateField(f, connKeys); err != nil {
			return fmt.Errorf("connection_fields[%d]: %w", i, err)
		}
	}
	pointKeys := map[string]bool{}
	for i, f := range in.PointFields {
		if err := validateField(f, pointKeys); err != nil {
			return fmt.Errorf("point_fields[%d]: %w", i, err)
		}
	}
	return nil
}

// validateField validates one field descriptor; seen dedupes keys within the
// same field list.
func validateField(f FieldSpec, seen map[string]bool) error {
	key := strings.TrimSpace(f.Key)
	if key == "" {
		return fmt.Errorf("%w: field key is required", ErrInvalid)
	}
	if seen[key] {
		return fmt.Errorf("%w: duplicate field key %q", ErrInvalid, key)
	}
	seen[key] = true
	if strings.TrimSpace(f.Label) == "" {
		return fmt.Errorf("%w: field %q needs a label", ErrInvalid, key)
	}
	if !validKinds[f.Kind] {
		return fmt.Errorf("%w: field %q has unknown kind %q", ErrInvalid, key, f.Kind)
	}
	if f.Kind == "select" && len(f.Options) == 0 {
		return fmt.Errorf("%w: select field %q needs options", ErrInvalid, key)
	}
	for _, opt := range f.Options {
		if strings.TrimSpace(opt.Value) == "" {
			return fmt.Errorf("%w: field %q has an option with an empty value", ErrInvalid, key)
		}
	}
	if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
		return fmt.Errorf("%w: field %q min exceeds max", ErrInvalid, key)
	}
	return nil
}
