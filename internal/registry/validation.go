// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/nereidai/nereidlink/internal/jsonutil"
)

var transformTypes = map[string]bool{
	"identity":   true,
	"linear":     true,
	"table":      true,
	"expression": true,
	"enum":       true,
}

// neutralDataTypes are the ADR-0032 vendor-neutral point datatypes.
var neutralDataTypes = map[string]bool{
	"bool": true, "int": true, "uint": true,
	"float": true, "string": true, "binary": true,
}

// readWriteModes are the accepted point access modes; "" means unknown.
var readWriteModes = map[string]bool{
	"": true, "R": true, "W": true, "RW": true,
}

var credentialTypes = map[string]bool{
	"TOKEN":    true,
	"PASSWORD": true,
	"X509":     true,
	"JWT":      true,
}

// validateProfile validates a profile and its descriptors.
func validateProfile(input ProfileDTO) error {
	if strings.TrimSpace(input.Name) == "" {
		return invalid("profile name is required")
	}
	if len(input.Name) > 255 {
		return invalid("profile name is too long")
	}
	if len(input.Protocol) > 64 {
		return invalid("profile protocol family is too long")
	}
	if input.RuleChainGroupID != "" {
		if _, err := uuid.Parse(input.RuleChainGroupID); err != nil {
			return invalid("rule_chain_group_id must be a UUID")
		}
	}
	keys := map[string]bool{}
	for _, descriptor := range input.Descriptors {
		if err := validateDescriptor(descriptor); err != nil {
			return err
		}
		if keys[descriptor.MeasurementKey] {
			return invalid("duplicate measurement key " + descriptor.MeasurementKey)
		}
		keys[descriptor.MeasurementKey] = true
	}
	return validateConnectorTemplate(input)
}

// validateConnectorTemplate checks the optional connector template carried
// by a profile. The template JSON is opaque (the kernel never interprets
// connector protocols) but its top-level shape must be a config object and a
// mappings array so the dispatch-time merge is well defined, and a template
// body requires a connector_type.
func validateConnectorTemplate(input ProfileDTO) error {
	hasConfig := len(input.ConnectorConfigTemplate) > 0 &&
		string(input.ConnectorConfigTemplate) != "{}"
	hasMappings := len(input.ConnectorMappings) > 0 &&
		string(input.ConnectorMappings) != "[]"
	if strings.TrimSpace(input.ConnectorType) == "" {
		if hasConfig || hasMappings || input.ConnectorTypeVersion != "" {
			return invalid("connector template requires connector_type")
		}
		return nil
	}
	if len(input.ConnectorType) > 255 || len(input.ConnectorTypeVersion) > 64 {
		return invalid("connector type/version is too long")
	}
	if hasConfig {
		if err := jsonutil.ValidateConfigObject(input.ConnectorConfigTemplate); err != nil {
			return invalid(err.Error())
		}
	}
	if hasMappings {
		if err := jsonutil.ValidateMappingArray(input.ConnectorMappings); err != nil {
			return invalid(err.Error())
		}
	}
	return nil
}

// validateConnectorTemplateSchema runs the optional edge-registered
// JSON-Schema validation for a profile's connector config template. It is
// best-effort: with no wired validator or no registered schema for the
// type/version it accepts (unmanaged connector types keep working), and a
// violation is returned as an invalid-argument error. A blank version is
// resolved to the highest registered one exactly as for instance writes.
//
// The template is validated with presence-relaxed rules (ValidateOverride),
// not ValidateConfig: it is reusable and intentionally omits per-instance
// keys the platform injects at connector-create time, notably the EdgeX
// device_name. Type/shape constraints on the keys the template sets are
// still enforced.
func (a *Admin) validateConnectorTemplateSchema(ctx context.Context, tenantID string, input ProfileDTO) error {
	if a.connectorTemplateValidator == nil ||
		strings.TrimSpace(input.ConnectorType) == "" {
		return nil
	}
	config := input.ConnectorConfigTemplate
	if len(config) == 0 || string(config) == "{}" {
		return nil
	}
	version := strings.TrimSpace(input.ConnectorTypeVersion)
	resolved, err := a.connectorTemplateValidator.ResolveVersion(
		ctx, tenantID, input.ConnectorType, version)
	if err != nil {
		return err
	}
	if err := a.connectorTemplateValidator.ValidateOverride(
		ctx, tenantID, input.ConnectorType, resolved, config); err != nil {
		return invalid("connector template config: " + err.Error())
	}
	return nil
}

// normalizeProfileTemplate returns a non-null, valid JSON document for a
// profile template column, falling back to the supplied shape ("{}" for
// config, "[]" for mappings) when the input is absent or malformed. Malformed
// JSON has already been rejected by validateConnectorTemplate when present,
// so the fallback here only covers empty/nil input.
func normalizeProfileTemplate(raw json.RawMessage, fallback string) []byte {
	if len(raw) == 0 {
		return []byte(fallback)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return []byte(fallback)
	}
	return []byte(raw)
}

// validateDescriptor validates one descriptor.
func validateDescriptor(input DescriptorDTO) error {
	if strings.TrimSpace(input.MeasurementKey) == "" {
		return invalid("measurement key is required")
	}
	if !transformTypes[input.TransformType] {
		return invalid("unsupported transform type " + input.TransformType)
	}
	if input.Precision < -1 {
		return invalid("precision must be -1 or non-negative")
	}
	if input.ValidMin != nil && input.ValidMax != nil && *input.ValidMin > *input.ValidMax {
		return invalid("valid minimum cannot exceed valid maximum")
	}
	if input.ExpectedPeriodSeconds != nil && *input.ExpectedPeriodSeconds <= 0 {
		return invalid("expected reporting period must be a positive number of seconds")
	}
	if input.ToleranceMultiplier != 0 && input.ToleranceMultiplier < 1 {
		return invalid("silence tolerance multiplier must be at least 1")
	}
	switch input.TransformType {
	case "table":
		if len(input.Breakpoints) < 2 {
			return invalid("table transform requires at least two breakpoints")
		}
	case "expression":
		if strings.TrimSpace(input.Expression) == "" {
			return invalid("expression is required")
		}
	case "enum":
		if len(input.EnumMap) == 0 {
			return invalid("enum map is required")
		}
	}
	// Neutral spec fields (ADR-0032) are optional but, when present, must
	// stay within the vendor-neutral vocabulary so the profile remains
	// portable across edge implementations.
	if input.DataType != "" && !neutralDataTypes[input.DataType] {
		return invalid("unsupported point data_type " + input.DataType)
	}
	if !readWriteModes[input.ReadWrite] {
		return invalid("unsupported point read_write mode " + input.ReadWrite)
	}
	if len(input.Address) > 0 {
		if err := jsonutil.ValidateConfigObject(input.Address); err != nil {
			return invalid("point address must be a JSON object")
		}
	}
	return nil
}

// validateDevice validates a device input.
func validateDevice(input DeviceDTO) error {
	if strings.TrimSpace(input.CanonicalName) == "" {
		return invalid("device canonical name is required")
	}
	if len(input.CanonicalName) > 255 {
		return invalid("device canonical name is too long")
	}
	// Alias is a short operator-facing label; notes are free-form text kept
	// to a sane bound so a single row cannot hold an unbounded payload.
	if len(input.Alias) > 255 {
		return invalid("device alias is too long")
	}
	if len(input.Notes) > 4096 {
		return invalid("device notes are too long")
	}
	if input.ProfileID != "" {
		if _, err := uuid.Parse(input.ProfileID); err != nil {
			return invalid("profile_id must be a UUID")
		}
	}
	if input.RuleChainGroupID != "" {
		if _, err := uuid.Parse(input.RuleChainGroupID); err != nil {
			return invalid("rule_chain_group_id must be a UUID")
		}
	}
	return nil
}

// validateCredential validates a credential input.
func validateCredential(input CredentialDTO) error {
	if !credentialTypes[input.Type] {
		return invalid("unsupported credential type " + input.Type)
	}
	if strings.TrimSpace(input.CredentialsID) == "" {
		return invalid("credentials_id is required")
	}
	if len(input.CredentialsID) > 255 {
		return invalid("credentials_id is too long")
	}
	if input.Status != "" &&
		input.Status != CredentialActive &&
		input.Status != CredentialDisabled &&
		input.Status != CredentialExpired {
		return invalid("unsupported credential status " + input.Status)
	}
	if (input.Type == "PASSWORD") && input.CredentialsValue == "" {
		return invalid("password value is required")
	}
	if input.Type == "X509" {
		if input.Issue {
			// Platform issuance supplies the certificate itself; any
			// pasted value would be ambiguous and is rejected.
			if strings.TrimSpace(input.CredentialsValue) != "" {
				return invalid("issue mode generates the certificate; remove the pasted certificate")
			}
		} else if !strings.Contains(input.CredentialsValue, "BEGIN CERTIFICATE") {
			// The certificate PEM is stored so the mTLS path can map it
			// by fingerprint; accepting anything weaker would create an
			// X509 row that can never authenticate.
			return invalid("x509 certificate PEM is required")
		}
	}
	if input.Type != "X509" && input.Issue {
		return invalid("issue is only valid for X509 credentials")
	}
	if input.Type == "JWT" {
		jwksURI, _ := input.Extra["jwks_uri"].(string)
		if strings.TrimSpace(input.CredentialsValue) == "" && strings.TrimSpace(jwksURI) == "" {
			return invalid("jwt requires an inline verification key or a jwks_uri")
		}
		if jwksURI != "" {
			if err := validateJWKSURI(jwksURI); err != nil {
				return invalid(err.Error())
			}
		}
	}
	return nil
}

// invalid wraps a validation message.
func invalid(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidArgument, message)
}

// invalidf wraps a formatted validation message. It exists so callers that
// interpolate a column name or line number keep the same ErrInvalidArgument
// wrapping as invalid, and therefore the same 400 mapping at the API edge.
func invalidf(format string, args ...any) error {
	return invalid(fmt.Sprintf(format, args...))
}
