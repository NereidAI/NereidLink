// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// connector_template_validation_test.go locks down that a profile's
// connector config template is validated with presence-relaxed rules
// (ValidateOverride), so a reusable template that omits per-instance keys
// such as the EdgeX device_name is accepted, while type violations on the
// keys it does set are still rejected.

package registry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// fakeTemplateValidator records which validation entry point ran.
type fakeTemplateValidator struct {
	// overrideCalled counts ValidateOverride invocations.
	overrideCalled int
	// configCalled counts ValidateConfig invocations; it must stay zero.
	configCalled int
	// resolvedVersion is returned by ResolveVersion when the caller's
	// version is blank.
	resolvedVersion string
	// overrideErr, when non-nil, is returned by ValidateOverride.
	overrideErr error
}

// ValidateConfig records a full-document validation; it must never be
// used for a profile template.
func (f *fakeTemplateValidator) ValidateConfig(_ context.Context, _, _, _ string, _ json.RawMessage) error {
	f.configCalled++
	return nil
}

// ValidateOverride records a presence-relaxed validation.
func (f *fakeTemplateValidator) ValidateOverride(_ context.Context, _, _, _ string, _ json.RawMessage) error {
	f.overrideCalled++
	return f.overrideErr
}

// ResolveVersion echoes an explicit version or returns the stubbed
// resolved one when the caller's version is blank.
func (f *fakeTemplateValidator) ResolveVersion(_ context.Context, _, _, version string) (string, error) {
	if version != "" {
		return version, nil
	}
	return f.resolvedVersion, nil
}

// TestValidateConnectorTemplateUsesOverride proves the profile template
// goes through the presence-relaxed entry point, never the full-document
// one.
func TestValidateConnectorTemplateUsesOverride(t *testing.T) {
	admin := NewAdmin(nil)
	fake := &fakeTemplateValidator{resolvedVersion: "3"}
	admin.SetConnectorTemplateValidator(fake)

	input := ProfileDTO{
		ConnectorType: "nereid-edge",
		// A reusable template with no device_name: previously rejected by
		// the full-config validator; must pass now.
		ConnectorConfigTemplate: json.RawMessage(
			`{"ds_name":"edgex-device","profile_name":"S7-Device"}`),
	}
	if err := admin.validateConnectorTemplateSchema(context.Background(),
		"tenant-1", input); err != nil {
		t.Fatalf("template without device_name must validate: %v", err)
	}
	if fake.overrideCalled != 1 {
		t.Fatalf("want exactly one ValidateOverride call, got %d", fake.overrideCalled)
	}
	if fake.configCalled != 0 {
		t.Fatalf("ValidateConfig must not be used for a template, got %d", fake.configCalled)
	}
}

// TestValidateConnectorTemplateOverrideError propagates a type violation
// from the relaxed validator so shape errors are still caught.
func TestValidateConnectorTemplateOverrideError(t *testing.T) {
	admin := NewAdmin(nil)
	want := errors.New("device_name must be string")
	fake := &fakeTemplateValidator{overrideErr: want}
	admin.SetConnectorTemplateValidator(fake)

	input := ProfileDTO{
		ConnectorType:           "nereid-edge",
		ConnectorConfigTemplate: json.RawMessage(`{"device_name": 7}`),
	}
	err := admin.validateConnectorTemplateSchema(context.Background(),
		"tenant-1", input)
	if err == nil {
		t.Fatal("a type violation must be returned")
	}
}

// TestValidateConnectorTemplateSkipsWithoutTypeOrConfig confirms the
// optional validator is not consulted when there is no connector type or
// no template body.
func TestValidateConnectorTemplateSkipsWithoutTypeOrConfig(t *testing.T) {
	admin := NewAdmin(nil)
	fake := &fakeTemplateValidator{}
	admin.SetConnectorTemplateValidator(fake)

	for _, input := range []ProfileDTO{
		{ConnectorType: "", ConnectorConfigTemplate: json.RawMessage(`{"x":1}`)},
		{ConnectorType: "nereid-edge", ConnectorConfigTemplate: json.RawMessage(`{}`)},
	} {
		if err := admin.validateConnectorTemplateSchema(context.Background(),
			"tenant-1", input); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if fake.overrideCalled != 0 {
		t.Fatalf("validator must not be called, got %d", fake.overrideCalled)
	}
}
