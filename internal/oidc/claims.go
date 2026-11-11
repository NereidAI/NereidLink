// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package oidc

import (
	"encoding/json"
	"strings"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
)

// idClaims is the subset of the id token the JIT path consumes.
type idClaims struct {
	email         string
	name          string
	emailVerified bool
	groups        []string
}

// decodeClaims verifies and extracts the claims the provisioner needs. The
// groups claim is intentionally free-form (string array or a single string)
// because IdPs vary (Keycloak "groups", Entra "groups" object ids, a custom
// claim), and the configured group_role map performs the translation.
func decodeClaims(token *coreoidc.IDToken, groupsClaim string) (idClaims, error) {
	var raw map[string]any
	if err := token.Claims(&raw); err != nil {
		return idClaims{}, err
	}

	out := idClaims{
		email: claimString(raw, "email"),
		name:  firstNonEmpty(claimString(raw, "name"), claimString(raw, "preferred_username")),
	}
	if v, ok := raw["email_verified"].(bool); ok {
		out.emailVerified = v
	}
	out.groups = claimGroups(raw[groupsClaim])
	return out, nil
}

func claimString(raw map[string]any, key string) string {
	if v, ok := raw[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// claimGroups normalizes the group claim to a string slice. go-oidc has
// already verified the signature; here we only tolerate shape variation.
func claimGroups(v any) []string {
	switch g := v.(type) {
	case []string:
		return g
	case []any:
		out := make([]string, 0, len(g))
		for _, item := range g {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if g == "" {
			return nil
		}
		// Some IdPs send a JSON-encoded array or a delimited string.
		var arr []string
		if strings.HasPrefix(g, "[") {
			if err := json.Unmarshal([]byte(g), &arr); err == nil {
				return arr
			}
		}
		return []string{g}
	default:
		return nil
	}
}
