// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package module

import (
	"fmt"
	"regexp"
)

// ingressPattern bounds ingress IDs to a safe subject suffix: lowercase
// alphanumerics, dashes and dots.
var ingressPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// validateCreate checks a provisioning request before it is written.
func validateCreate(input CreateInput) error {
	switch input.Type {
	case TypeCoAP, TypeLwM2M, TypeSNMP:
	default:
		return fmt.Errorf("%w: unsupported module type %q",
			ErrInvalidArgument, input.Type)
	}
	if input.Name == "" {
		return fmt.Errorf("%w: module name is required", ErrInvalidArgument)
	}
	if !ingressPattern.MatchString(input.IngressID) {
		return fmt.Errorf("%w: invalid ingress id", ErrInvalidArgument)
	}
	return nil
}
