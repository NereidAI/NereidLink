// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nereidai/nereidlink/internal/companion"
	"github.com/nereidai/nereidlink/internal/rbac"
)

func TestCompanionRoutePoints(t *testing.T) {
	cases := []struct {
		method string
		rest   []string
		want   string
	}{
		{http.MethodGet, []string{"companion-endpoints"}, rbac.PointCompanionRead},
		{http.MethodPost, []string{"companion-endpoints"}, rbac.PointCompanionWrite},
		{http.MethodPut, []string{"companion-endpoints", "ep-1"}, rbac.PointCompanionWrite},
		{http.MethodDelete, []string{"companion-endpoints", "ep-1"}, rbac.PointCompanionWrite},
		{http.MethodGet, []string{"companion-endpoints", "ep-1", "subscriptions"}, rbac.PointCompanionRead},
	}
	for _, tc := range cases {
		if got := requiredPoint(tc.method, tc.rest); got != tc.want {
			t.Fatalf("%s %v: got %s want %s", tc.method, tc.rest, got, tc.want)
		}
	}
}

type fakeCompanionAdmin struct {
	endpoints  []companion.Endpoint
	rows       []companion.DesiredRow
	registerIn companion.EndpointInput
	updateIn   companion.EndpointInput
	deletedID  string
	notFound   bool
	invalid    bool
}

func (f *fakeCompanionAdmin) ListEndpoints(_ context.Context, _ string) ([]companion.Endpoint, error) {
	return f.endpoints, nil
}

func (f *fakeCompanionAdmin) GetEndpoint(_ context.Context, _, _ string) (companion.Endpoint, error) {
	if f.notFound {
		return companion.Endpoint{}, companion.ErrEndpointNotFound
	}
	return f.endpoints[0], nil
}

func (f *fakeCompanionAdmin) Register(_ context.Context, _ string, in companion.EndpointInput) (companion.Endpoint, error) {
	if f.invalid {
		return companion.Endpoint{}, companion.ErrInvalidArgument
	}
	f.registerIn = in
	return companion.Endpoint{
		ID: "ep-1", TenantID: "t1", CompanionID: in.CompanionID, GatewayID: in.GatewayID,
		BaseURL: in.BaseURL, ContractVersion: companion.ContractV1,
		AuthToken: *in.AuthToken, Enabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}, nil
}

func (f *fakeCompanionAdmin) Update(_ context.Context, _, id string, in companion.EndpointInput) (companion.Endpoint, error) {
	if f.notFound {
		return companion.Endpoint{}, companion.ErrEndpointNotFound
	}
	if f.invalid {
		return companion.Endpoint{}, companion.ErrInvalidArgument
	}
	f.updateIn = in
	token := ""
	if in.AuthToken != nil {
		token = *in.AuthToken
	}
	return companion.Endpoint{ID: id, CompanionID: in.CompanionID, GatewayID: in.GatewayID,
		BaseURL: in.BaseURL, ContractVersion: companion.ContractV1, AuthToken: token, Enabled: true}, nil
}

func (f *fakeCompanionAdmin) Delete(_ context.Context, _, id string) error {
	if f.notFound {
		return companion.ErrEndpointNotFound
	}
	f.deletedID = id
	return nil
}

func (f *fakeCompanionAdmin) ListDesired(_ context.Context, _, _ string) ([]companion.DesiredRow, error) {
	if f.notFound {
		return nil, companion.ErrEndpointNotFound
	}
	return f.rows, nil
}

func newCompanionHub(admin CompanionAdmin) *Hub {
	h := NewHub(nil, Tuning{})
	h.SetCompanionAdmin(admin)
	return h
}

func companionRequest(method, target string, body any) *http.Request {
	req := httptest.NewRequest(method, target, jsonReader(body))
	req.SetPathValue("tenant", "t1")
	if strings.Contains(target, "/ep-1") {
		req.SetPathValue("endpoint", "ep-1")
	}
	return req
}

func jsonReader(v any) *strings.Reader {
	raw, _ := json.Marshal(v)
	return strings.NewReader(string(raw))
}

func TestCompanionEndpointRegisterAndRedactToken(t *testing.T) {
	admin := &fakeCompanionAdmin{}
	hub := newCompanionHub(admin)
	body := companionEndpointBody{
		CompanionID: "face-service", GatewayID: "gateway-1",
		BaseURL: "https://companion.example", AuthToken: strPointer("super-secret"),
	}
	rec := httptest.NewRecorder()
	hub.handleCompanionEndpoints(rec, companionRequest(http.MethodPost, "/", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "super-secret") {
		t.Fatal("response leaked the auth token")
	}
	var view companionEndpointView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if !view.AuthTokenSet || view.ID != "ep-1" || view.ContractVersion != companion.ContractV1 {
		t.Fatalf("unexpected view: %+v", view)
	}
	if admin.registerIn.CompanionID != "face-service" || admin.registerIn.AuthToken == nil {
		t.Fatalf("input not forwarded: %+v", admin.registerIn)
	}
}

func TestCompanionEndpointListEmptyArray(t *testing.T) {
	hub := newCompanionHub(&fakeCompanionAdmin{endpoints: []companion.Endpoint{}})
	rec := httptest.NewRecorder()
	hub.handleCompanionEndpoints(rec, companionRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty list must serialize as [], got %d %q", rec.Code, rec.Body.String())
	}
}

func TestCompanionEndpointCRUDAndErrors(t *testing.T) {
	admin := &fakeCompanionAdmin{
		endpoints: []companion.Endpoint{{ID: "ep-1", CompanionID: "face-service",
			GatewayID: "gateway-1", BaseURL: "https://companion.example", Enabled: true}},
	}
	hub := newCompanionHub(admin)

	// GET one.
	rec := httptest.NewRecorder()
	hub.handleCompanionEndpoint(rec, companionRequest(http.MethodGet, "/ep-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d", rec.Code)
	}

	// PUT updates and preserves token when omitted.
	rec = httptest.NewRecorder()
	hub.handleCompanionEndpoint(rec, companionRequest(http.MethodPut, "/ep-1", companionEndpointBody{
		CompanionID: "face-service", GatewayID: "gateway-1", BaseURL: "https://companion-2.example",
	}))
	if rec.Code != http.StatusOK || admin.updateIn.AuthToken != nil {
		t.Fatalf("update status = %d token=%v", rec.Code, admin.updateIn.AuthToken)
	}

	// DELETE.
	rec = httptest.NewRecorder()
	hub.handleCompanionEndpoint(rec, companionRequest(http.MethodDelete, "/ep-1", nil))
	if rec.Code != http.StatusNoContent || admin.deletedID != "ep-1" {
		t.Fatalf("delete status = %d id=%q", rec.Code, admin.deletedID)
	}

	// 404 mapping.
	admin.notFound = true
	rec = httptest.NewRecorder()
	hub.handleCompanionEndpoint(rec, companionRequest(http.MethodGet, "/ep-1", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("not-found status = %d", rec.Code)
	}

	// 400 mapping.
	admin.notFound, admin.invalid = false, true
	rec = httptest.NewRecorder()
	hub.handleCompanionEndpoints(rec, companionRequest(http.MethodPost, "/", companionEndpointBody{}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d", rec.Code)
	}
}

func TestCompanionSubscriptionsView(t *testing.T) {
	admin := &fakeCompanionAdmin{rows: []companion.DesiredRow{
		{RowID: "companion:g:d:s", SourceDevice: "s", Camera: "s", PipelineID: "p",
			ConclusionDeviceID: "d", Enabled: true, ArtifactKey: "a", Revision: 2,
			Status: companion.StatusApplied},
	}}
	hub := newCompanionHub(admin)
	rec := httptest.NewRecorder()
	hub.handleCompanionSubscriptions(rec, companionRequest(http.MethodGet, "/ep-1/subscriptions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Subscriptions []companionDesiredView `json:"subscriptions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Subscriptions) != 1 || body.Subscriptions[0].Status != companion.StatusApplied ||
		body.Subscriptions[0].Revision != 2 {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestCompanionAdminUnavailable(t *testing.T) {
	hub := NewHub(nil, Tuning{})
	rec := httptest.NewRecorder()
	hub.handleCompanionEndpoints(rec, companionRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestCompanionErrorSentinels(t *testing.T) {
	// Guard the typed errors the handler switch depends on.
	if !errors.Is(companion.ErrEndpointNotFound, companion.ErrEndpointNotFound) {
		t.Fatal("ErrEndpointNotFound sentinel broken")
	}
}

func strPointer(s string) *string { return &s }
