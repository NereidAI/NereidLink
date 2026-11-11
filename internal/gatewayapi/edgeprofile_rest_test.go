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

	"github.com/nereidai/nereidlink/internal/edgeprofile"
	"github.com/nereidai/nereidlink/internal/registry"
)

type fakeEdgeProfileAdmin struct {
	list     []edgeprofile.CachedProfile
	listErr  error
	result   edgeprofile.AdoptResult
	adoptErr error
	gotReq   edgeprofile.AdoptRequest
}

func (f *fakeEdgeProfileAdmin) ListForGateway(_ context.Context, _, gatewayID string) ([]edgeprofile.CachedProfile, error) {
	f.gotReq = edgeprofile.AdoptRequest{GatewayID: gatewayID}
	return f.list, f.listErr
}

func (f *fakeEdgeProfileAdmin) Detail(_ context.Context, _, gatewayID, name string) (edgeprofile.Detail, error) {
	return edgeprofile.Detail{
		Cached: edgeprofile.CachedProfile{GatewayID: gatewayID, Name: name, ResourceCount: 1},
		Resources: []edgeprofile.ResourceClass{
			{Resource: edgeprofile.Resource{Name: "b", ValueType: "Bool"}, Translatable: true},
			{Resource: edgeprofile.Resource{Name: "arr", ValueType: "BoolArray"}, SkipReason: "x"},
		},
	}, nil
}

func (f *fakeEdgeProfileAdmin) Adopt(_ context.Context, _ string, req edgeprofile.AdoptRequest) (edgeprofile.AdoptResult, error) {
	f.gotReq = req
	return f.result, f.adoptErr
}

func (f *fakeEdgeProfileAdmin) ImportDocument(_ context.Context, _ string, _ []byte) (edgeprofile.ImportResult, error) {
	return edgeprofile.ImportResult{ProfileID: "imp-1", PointCount: 1, Warnings: []string{}}, nil
}

func (f *fakeEdgeProfileAdmin) PreviewDocument(_ []byte) (edgeprofile.ImportResult, error) {
	return edgeprofile.ImportResult{PointCount: 1, Warnings: []string{}}, nil
}

func newEdgeProfileHub(admin EdgeProfileAdmin) *Hub {
	h := NewHub(nil, Tuning{})
	h.SetEdgeProfileAdmin(admin)
	return h
}

func TestHandleEdgeProfilesListEmptyArray(t *testing.T) {
	h := newEdgeProfileHub(&fakeEdgeProfileAdmin{list: []edgeprofile.CachedProfile{}})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetPathValue("tenant", "t1")
	req.SetPathValue("gateway", "gw1")
	rec := httptest.NewRecorder()
	h.handleEdgeProfiles(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	// Empty lists must serialize as [] never null.
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty list must be [], got %q", rec.Body.String())
	}
}

func TestHandleEdgeProfilesMethodNotAllowed(t *testing.T) {
	h := newEdgeProfileHub(&fakeEdgeProfileAdmin{})
	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	rec := httptest.NewRecorder()
	h.handleEdgeProfiles(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestHandleEdgeProfileAdoptCreated(t *testing.T) {
	admin := &fakeEdgeProfileAdmin{
		result: edgeprofile.AdoptResult{
			ProfileID: "p1", ProfileName: "Temp", Created: true,
			AdoptedCount: 2,
			Skipped: []edgeprofile.SkippedResource{
				{Name: "blob", ValueType: "Binary", Reason: "x"},
			},
		},
	}
	h := newEdgeProfileHub(admin)
	req := httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"mode":"update","target_name":"Temp-edge"}`))
	req.SetPathValue("tenant", "t1")
	req.SetPathValue("gateway", "gw1")
	req.SetPathValue("profile", "Temp")
	rec := httptest.NewRecorder()
	h.handleEdgeProfileAdopt(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if admin.gotReq.Mode != edgeprofile.AdoptUpdate ||
		admin.gotReq.TargetName != "Temp-edge" ||
		admin.gotReq.GatewayID != "gw1" || admin.gotReq.Name != "Temp" {
		t.Fatalf("adopt request not passed through: %+v", admin.gotReq)
	}
	var resp adoptEdgeProfileResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Created || resp.AdoptedCount != 2 || len(resp.Skipped) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestHandleEdgeProfileAdoptEmptyBodyNoop(t *testing.T) {
	admin := &fakeEdgeProfileAdmin{
		result: edgeprofile.AdoptResult{ProfileID: "p1", ProfileName: "Temp"},
	}
	h := newEdgeProfileHub(admin)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.ContentLength = 0
	req.SetPathValue("tenant", "t1")
	req.SetPathValue("gateway", "gw1")
	req.SetPathValue("profile", "Temp")
	rec := httptest.NewRecorder()
	h.handleEdgeProfileAdopt(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("idempotent no-op status = %d", rec.Code)
	}
}

func TestWriteEdgeProfileErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{edgeprofile.ErrNotFound, http.StatusNotFound},
		{edgeprofile.ErrInvalid, http.StatusBadRequest},
		{registry.ErrConflict, http.StatusConflict},
		{registry.ErrInvalidArgument, http.StatusBadRequest},
		{errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		writeEdgeProfileError(rec, tc.err)
		if rec.Code != tc.want {
			t.Fatalf("%v: got %d want %d", tc.err, rec.Code, tc.want)
		}
	}
}

func TestHandleEdgeProfileDetail(t *testing.T) {
	h := newEdgeProfileHub(&fakeEdgeProfileAdmin{})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetPathValue("tenant", "t1")
	req.SetPathValue("gateway", "gw1")
	req.SetPathValue("profile", "Temp")
	rec := httptest.NewRecorder()
	h.handleEdgeProfileDetail(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got edgeProfileDetailDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Temp" || len(got.Resources) != 2 {
		t.Fatalf("unexpected detail: %+v", got)
	}
	if !got.Resources[0].Translatable || got.Resources[1].Translatable {
		t.Fatalf("resource classification wrong: %+v", got.Resources)
	}
}

func TestEdgeProfileServiceUnavailable(t *testing.T) {
	h := NewHub(nil, Tuning{})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.handleEdgeProfiles(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d want 503", rec.Code)
	}
}
