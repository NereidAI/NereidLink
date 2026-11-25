// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Regression tests for the Ingress path split. The NS ticket SSO callback is
// the only browser-facing backend route outside /v1, and it must be an Exact
// match: a broad /auth Prefix also captures the SPA route /auth/callback that
// handleSSOTicket redirects to after ticket verification. In the split
// topology that request reached the backend, whose Go embed holds only the
// dist/.gitkeep placeholder, so http.FileServer answered with a directory
// autoindex instead of the console. The static test runs everywhere; the
// render test additionally needs the helm binary and skips without it.
package nereidlink_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ingressPath models the networking.k8s.io/v1 Ingress path fields asserted on.
type ingressPath struct {
	Path     string `yaml:"path"`
	PathType string `yaml:"pathType"`
	Backend  struct {
		Service struct {
			Name string `yaml:"name"`
			Port struct {
				Number int `yaml:"number"`
			} `yaml:"port"`
		} `yaml:"service"`
	} `yaml:"backend"`
}

// renderedIngress models the rendered Ingress document.
type renderedIngress struct {
	Kind string `yaml:"kind"`
	Spec struct {
		Rules []struct {
			HTTP struct {
				Paths []ingressPath `yaml:"paths"`
			} `yaml:"http"`
		} `yaml:"rules"`
	} `yaml:"spec"`
}

// baseRenderValues satisfies the chart's always-required blob credentials.
// PostgreSQL and NATS use their bundled defaults, so no extra values are
// needed for `helm template`.
const baseRenderValues = `
ingress:
  enabled: true
  host: nereidlink.example.com
blob:
  endpoint: "seaweedfs.svc.cluster.local:8333"
  accessKey: "test-access-key"
  secretKey: "test-secret-key"
`

// ssoRenderValues enables NS ticket SSO with the default callback path.
const ssoRenderValues = baseRenderValues + `
sso:
  enabled: true
  nsBaseURL: "https://nereidsphere.example.com"
  clientID: "link"
  ssoSecret: "test-sso-secret"
  callbackPath: "/auth/ns-callback"
  tenantID: "00000000-0000-0000-0000-000000000001"
`

// TestIngressSSOCallbackTemplateIsExact guards the template source without
// needing the helm binary, so the assertion also runs in CI.
func TestIngressSSOCallbackTemplateIsExact(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("templates", "ingress.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)

	if strings.Contains(text, "- path: /auth\n") {
		t.Fatal("ingress.yaml still renders a broad /auth Prefix route; " +
			"use an Exact match on .Values.sso.callbackPath so /auth/callback stays on the frontend")
	}
	if !strings.Contains(text, "{{- if .Values.sso.enabled }}") {
		t.Fatal("the SSO callback route must be rendered only when sso.enabled")
	}
	callbackRoute := "- path: {{ .Values.sso.callbackPath | quote }}\n            pathType: Exact"
	if !strings.Contains(text, callbackRoute) {
		t.Fatalf("ingress.yaml must render sso.callbackPath with pathType Exact; want substring %q", callbackRoute)
	}
}

// TestHelmRenderSSORouting renders the chart and inspects the resulting
// Ingress path table. It skips when helm is not installed (the CI runners
// only carry the Go toolchain; the static test above still covers them).
func TestHelmRenderSSORouting(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm binary not found in PATH")
	}

	t.Run("sso enabled routes only the exact callback to the backend", func(t *testing.T) {
		ing := renderIngress(t, ssoRenderValues)
		paths := ing.Spec.Rules[0].HTTP.Paths

		backend := backendServiceName(t, paths, "/v1")
		frontend := frontendServiceName(t, paths)
		if backend == frontend {
			t.Fatalf("backend and frontend resolve to the same Service %q", backend)
		}

		var callback *ingressPath
		for i := range paths {
			p := &paths[i]
			if strings.HasPrefix(p.Path, "/auth") {
				if p.Path != "/auth/ns-callback" {
					t.Fatalf("unexpected /auth backend route %q (%s); only the SSO callback may reach the backend",
						p.Path, p.PathType)
				}
				callback = p
			}
		}
		if callback == nil {
			t.Fatal("SSO enabled but no /auth/ns-callback Ingress path was rendered")
		}
		if callback.PathType != "Exact" {
			t.Fatalf("callback pathType = %q, want Exact", callback.PathType)
		}
		if callback.Backend.Service.Name != backend || callback.Backend.Service.Port.Number != 8080 {
			t.Fatalf("callback backend = %s:%d, want %s:8080",
				callback.Backend.Service.Name, callback.Backend.Service.Port.Number, backend)
		}
	})

	t.Run("custom callback path is an exact backend route", func(t *testing.T) {
		values := strings.Replace(ssoRenderValues,
			`callbackPath: "/auth/ns-callback"`,
			`callbackPath: "/custom/sso-callback"`, 1)
		ing := renderIngress(t, values)
		paths := ing.Spec.Rules[0].HTTP.Paths

		var found bool
		for _, p := range paths {
			if strings.HasPrefix(p.Path, "/auth") {
				t.Fatalf("default /auth route rendered with custom callbackPath: %q", p.Path)
			}
			if p.Path == "/custom/sso-callback" {
				found = true
				if p.PathType != "Exact" {
					t.Fatalf("custom callback pathType = %q, want Exact", p.PathType)
				}
			}
		}
		if !found {
			t.Fatal("custom sso.callbackPath was not rendered as an Ingress path")
		}
	})

	t.Run("sso disabled renders no auth backend route", func(t *testing.T) {
		ing := renderIngress(t, baseRenderValues)
		for _, p := range ing.Spec.Rules[0].HTTP.Paths {
			if strings.HasPrefix(p.Path, "/auth") {
				t.Fatalf("SSO disabled but Ingress renders backend route %q", p.Path)
			}
		}
		if frontendServiceName(t, ing.Spec.Rules[0].HTTP.Paths) == "" {
			t.Fatal("the frontend catch-all route must still be rendered")
		}
	})

	t.Run("helm lint passes with sso values", func(t *testing.T) {
		valuesFile := writeValues(t, ssoRenderValues)
		cmd := exec.Command("helm", "lint", ".", "-f", valuesFile)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("helm lint failed: %v\n%s", err, out)
		}
	})
}

// renderIngress templates the chart with the given values and returns the
// rendered Ingress document.
func renderIngress(t *testing.T, values string) renderedIngress {
	t.Helper()
	valuesFile := writeValues(t, values)
	cmd := exec.Command("helm", "template", "nereidlink", ".", "-f", valuesFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}

	var found bool
	var ing renderedIngress
	for _, doc := range strings.Split(string(out), "\n---\n") {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var candidate renderedIngress
		if err := yaml.Unmarshal([]byte(doc), &candidate); err != nil {
			t.Fatalf("unmarshal rendered document: %v\n%s", err, doc)
		}
		if candidate.Kind == "Ingress" {
			ing = candidate
			found = true
		}
	}
	if !found {
		t.Fatal("no Ingress document found in rendered manifests")
	}
	if len(ing.Spec.Rules) == 0 || len(ing.Spec.Rules[0].HTTP.Paths) == 0 {
		t.Fatal("rendered Ingress has no rules or paths")
	}
	return ing
}

// writeValues writes values YAML into a per-test temp directory.
func writeValues(t *testing.T, values string) string {
	t.Helper()
	valuesFile := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(valuesFile, []byte(values), 0o600); err != nil {
		t.Fatal(err)
	}
	return valuesFile
}

// backendServiceName returns the Service backing the given backend prefix.
func backendServiceName(t *testing.T, paths []ingressPath, prefix string) string {
	t.Helper()
	for _, p := range paths {
		if p.Path == prefix {
			return p.Backend.Service.Name
		}
	}
	t.Fatalf("backend route %q not found in rendered Ingress", prefix)
	return ""
}

// frontendServiceName returns the Service backing the "/" catch-all route.
func frontendServiceName(t *testing.T, paths []ingressPath) string {
	t.Helper()
	for _, p := range paths {
		if p.Path == "/" && p.PathType == "Prefix" {
			return p.Backend.Service.Name
		}
	}
	t.Fatal("frontend catch-all route \"/\" not found in rendered Ingress")
	return ""
}
