// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package gatewayapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/nereidai/nereidlink/internal/business"
	"github.com/nereidai/nereidlink/internal/rbac"
)

func TestBusinessPackRoutePoints(t *testing.T) {
	cases := []struct {
		method string
		rest   []string
		want   string
	}{
		{http.MethodPost, []string{"business-templates", "import"}, rbac.PointBusinessWrite},
		{http.MethodGet, []string{"business-templates", "export"}, rbac.PointBusinessRead},
		{http.MethodGet, []string{"business-templates"}, rbac.PointBusinessRead},
		{http.MethodPost, []string{"business-templates"}, rbac.PointBusinessWrite},
	}
	for _, tc := range cases {
		if got := requiredPoint(tc.method, tc.rest); got != tc.want {
			t.Fatalf("%s %v: got %s want %s", tc.method, tc.rest, got, tc.want)
		}
	}
}

type fakeBusinessAdmin struct {
	importRaw  []byte
	importErr  error
	imported   []business.Template
	exportPack business.TemplatePack
	exportErr  error
}

func (f *fakeBusinessAdmin) ImportTemplates(_ context.Context, _, _ string, raw []byte) ([]business.Template, error) {
	f.importRaw = raw
	if f.importErr != nil {
		return nil, f.importErr
	}
	return f.imported, nil
}

func (f *fakeBusinessAdmin) ExportTemplates(_ context.Context, _ string) (business.TemplatePack, error) {
	if f.exportErr != nil {
		return business.TemplatePack{}, f.exportErr
	}
	return f.exportPack, nil
}

func (f *fakeBusinessAdmin) ListTemplates(context.Context, string) ([]business.Template, error) {
	return nil, nil
}
func (f *fakeBusinessAdmin) CreateTemplate(context.Context, string, string, business.Template) (business.Template, error) {
	return business.Template{}, nil
}
func (f *fakeBusinessAdmin) DeleteTemplate(context.Context, string, string, string) error { return nil }
func (f *fakeBusinessAdmin) ListInstances(context.Context, string) ([]business.Instance, error) {
	return nil, nil
}
func (f *fakeBusinessAdmin) CreateInstance(context.Context, string, business.InstanceInput) (business.Instance, error) {
	return business.Instance{}, nil
}
func (f *fakeBusinessAdmin) GetInstance(context.Context, string, string) (business.Instance, error) {
	return business.Instance{}, nil
}
func (f *fakeBusinessAdmin) UpdateInstance(context.Context, string, string, business.InstanceUpdate) (business.Instance, error) {
	return business.Instance{}, nil
}
func (f *fakeBusinessAdmin) DeleteInstance(context.Context, string, string) error { return nil }
func (f *fakeBusinessAdmin) ValidateInstance(context.Context, string, string) (business.ValidationReport, error) {
	return business.ValidationReport{}, nil
}
func (f *fakeBusinessAdmin) Expansion(context.Context, string, string, int64) (business.Expansion, error) {
	return business.Expansion{}, nil
}
func (f *fakeBusinessAdmin) Deploy(context.Context, string, string) (business.Instance, error) {
	return business.Instance{}, nil
}
func (f *fakeBusinessAdmin) Undeploy(context.Context, string, string) (business.Instance, error) {
	return business.Instance{}, nil
}
func (f *fakeBusinessAdmin) Rollback(context.Context, string, string, int64) (business.Instance, error) {
	return business.Instance{}, nil
}
func (f *fakeBusinessAdmin) Monitor(context.Context, string, string) (business.Monitor, error) {
	return business.Monitor{}, nil
}

func newBusinessPackHub(admin BusinessAdmin) *Hub {
	h := NewHub(nil, Tuning{})
	h.SetBusinessAdmin(admin)
	return h
}

func businessPackRequest(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.SetPathValue("tenant", "t1")
	return req
}

func TestBusinessTemplateImportForwardsRawPack(t *testing.T) {
	admin := &fakeBusinessAdmin{imported: []business.Template{{ID: "face-access-control", Version: "0.0.1"}}}
	hub := newBusinessPackHub(admin)
	rec := httptest.NewRecorder()
	hub.handleBusinessTemplateImport(rec, businessPackRequest(http.MethodPost,
		"/v1/tenants/t1/business-templates/import", "id: face-access-control\nversion: 0.0.1\n"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(string(admin.importRaw), "face-access-control") {
		t.Fatalf("raw pack not forwarded: %q", admin.importRaw)
	}
	var created []business.Template
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0].ID != "face-access-control" {
		t.Fatalf("unexpected response: %+v", created)
	}
}

func TestBusinessTemplateImportMapsDomainErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"invalid", business.ErrInvalid, http.StatusBadRequest},
		{"conflict", business.ErrConflict, http.StatusConflict},
		{"not found", business.ErrNotFound, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub := newBusinessPackHub(&fakeBusinessAdmin{importErr: tc.err})
			rec := httptest.NewRecorder()
			hub.handleBusinessTemplateImport(rec, businessPackRequest(http.MethodPost,
				"/v1/tenants/t1/business-templates/import", "templates: []\n"))
			if rec.Code != tc.want {
				t.Fatalf("status = %d want %d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestBusinessTemplateImportRejectsOversizedPack(t *testing.T) {
	admin := &fakeBusinessAdmin{}
	hub := newBusinessPackHub(admin)
	rec := httptest.NewRecorder()
	hub.handleBusinessTemplateImport(rec, businessPackRequest(http.MethodPost,
		"/v1/tenants/t1/business-templates/import", strings.Repeat("x", maxTemplatePackBytes+1)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized pack status = %d want %d", rec.Code, http.StatusBadRequest)
	}
	if admin.importRaw != nil {
		t.Fatal("oversized pack must not reach the domain service")
	}
}

func TestBusinessTemplateImportMethodAndAvailability(t *testing.T) {
	hub := newBusinessPackHub(&fakeBusinessAdmin{})
	rec := httptest.NewRecorder()
	hub.handleBusinessTemplateImport(rec, businessPackRequest(http.MethodGet, "/", ""))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET import status = %d", rec.Code)
	}

	bare := NewHub(nil, Tuning{})
	rec = httptest.NewRecorder()
	bare.handleBusinessTemplateImport(rec, businessPackRequest(http.MethodPost, "/", "{}"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired module status = %d", rec.Code)
	}
}

func TestBusinessTemplateExportYAMLAndJSON(t *testing.T) {
	pack := business.TemplatePack{Templates: []business.Template{{
		SchemaVersion: business.TemplateSchemaVersion, ID: "meter-reading", Version: "1.0.0",
		Name: "Meter reading",
	}}}
	hub := newBusinessPackHub(&fakeBusinessAdmin{exportPack: pack})

	rec := httptest.NewRecorder()
	hub.handleBusinessTemplateExport(rec, businessPackRequest(http.MethodGet,
		"/v1/tenants/t1/business-templates/export", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("yaml export status = %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/yaml") {
		t.Fatalf("yaml content type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "business-templates.yaml") {
		t.Fatalf("content disposition = %q", cd)
	}
	var decoded business.TemplatePack
	if err := yaml.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("exported body is not parseable YAML: %v", err)
	}
	if len(decoded.Templates) != 1 || decoded.Templates[0].ID != "meter-reading" {
		t.Fatalf("unexpected yaml pack: %+v", decoded)
	}

	rec = httptest.NewRecorder()
	hub.handleBusinessTemplateExport(rec, businessPackRequest(http.MethodGet,
		"/v1/tenants/t1/business-templates/export?format=json", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("json export status = %d", rec.Code)
	}
	var jsonPack business.TemplatePack
	if err := json.Unmarshal(rec.Body.Bytes(), &jsonPack); err != nil {
		t.Fatalf("json export not parseable: %v", err)
	}
	if jsonPack.Templates[0].ID != "meter-reading" {
		t.Fatalf("unexpected json pack: %+v", jsonPack)
	}

	rec = httptest.NewRecorder()
	hub.handleBusinessTemplateExport(rec, businessPackRequest(http.MethodGet,
		"/v1/tenants/t1/business-templates/export?format=xml", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad format status = %d", rec.Code)
	}
}
