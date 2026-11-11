// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// accounting_handlers_test.go covers the ADR-0025 Top-N diagnostic API:
// parameter validation, the empty-result [] contract, exact-vs-estimated
// labelling and the tenant-filter scope rule.

package gatewayapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nereidai/nereidlink/internal/accounting"
)

// stubAccounting returns canned Top-N rows and records the query it saw.
type stubAccounting struct {
	results []accounting.TopResult
	err     error
	last    accounting.TopQuery
}

func (s *stubAccounting) Top(_ context.Context, q accounting.TopQuery) ([]accounting.TopResult, error) {
	s.last = q
	return s.results, s.err
}

// callTop issues a request against the handler and returns the recorder.
func callTop(t *testing.T, hub *Hub, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	hub.handleAccountingTopN(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// TestAccountingTopNDefaults verifies the default query.
func TestAccountingTopNDefaults(t *testing.T) {
	stub := &stubAccounting{results: []accounting.TopResult{
		{Value: "t1", MessageCount: 900, ByteCount: 9000, Nodes: 2, SampleRate: 4},
	}}
	hub := &Hub{}
	hub.SetAccounting(stub)

	rec := callTop(t, hub, "/v1/platform/accounting/topn")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if stub.last.Dimension != accounting.DimensionTenant {
		t.Fatalf("expected the tenant dimension by default, got %s", stub.last.Dimension)
	}
	if stub.last.Limit != 20 {
		t.Fatalf("expected the default limit 20, got %d", stub.last.Limit)
	}
	var payload accountingTopNResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(payload.Entries))
	}
	// A sampled report must be labelled as an estimate.
	if !payload.Entries[0].Estimated {
		t.Fatal("expected estimated=true for a sampled report")
	}
}

// TestAccountingTopNEmptyIsArray verifies an empty result is [] and never
// null, so clients can iterate without a nil check.
func TestAccountingTopNEmptyIsArray(t *testing.T) {
	hub := &Hub{}
	hub.SetAccounting(&stubAccounting{results: nil})

	rec := callTop(t, hub, "/v1/platform/accounting/topn")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload accountingTopNResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Entries == nil {
		t.Fatal("entries must be an empty array, not null")
	}
	if len(payload.Entries) != 0 {
		t.Fatalf("expected no entries, got %d", len(payload.Entries))
	}
}

// TestAccountingTopNExactLabel verifies an unsampled report is not
// labelled as an estimate.
func TestAccountingTopNExactLabel(t *testing.T) {
	hub := &Hub{}
	hub.SetAccounting(&stubAccounting{results: []accounting.TopResult{
		{Value: "t1", MessageCount: 10, SampleRate: 1},
	}})

	rec := callTop(t, hub, "/v1/platform/accounting/topn")
	var payload accountingTopNResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Entries[0].Estimated {
		t.Fatal("an unsampled report must not be marked estimated")
	}
}

// TestAccountingTopNRejectsBadParams verifies parameter validation.
func TestAccountingTopNRejectsBadParams(t *testing.T) {
	hub := &Hub{}
	hub.SetAccounting(&stubAccounting{})

	cases := []struct {
		target string
		code   string
	}{
		{"/v1/platform/accounting/topn?dimension=nonsense", "ACCOUNTING_BAD_DIMENSION"},
		{"/v1/platform/accounting/topn?window=3h", "ACCOUNTING_BAD_WINDOW"},
		{"/v1/platform/accounting/topn?limit=0", "ACCOUNTING_BAD_LIMIT"},
		{"/v1/platform/accounting/topn?limit=abc", "ACCOUNTING_BAD_LIMIT"},
		{"/v1/platform/accounting/topn?limit=-5", "ACCOUNTING_BAD_LIMIT"},
		// An oversized limit must be rejected rather than clamped, because
		// clamping would echo a limit the response does not honour.
		{"/v1/platform/accounting/topn?limit=201", "ACCOUNTING_BAD_LIMIT"},
		// The tenant filter only narrows the tenant dimension; accepting it
		// elsewhere would silently return the unfiltered ranking.
		{"/v1/platform/accounting/topn?dimension=device&tenant=t1", "ACCOUNTING_BAD_TENANT_FILTER"},
	}
	for _, tc := range cases {
		rec := callTop(t, hub, tc.target)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d", tc.target, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), tc.code) {
			t.Fatalf("%s: expected %s, got %s", tc.target, tc.code, rec.Body.String())
		}
	}
}

// TestAccountingTopNTenantFilterAccepted verifies the filter reaches the
// store for the tenant dimension.
func TestAccountingTopNTenantFilterAccepted(t *testing.T) {
	stub := &stubAccounting{}
	hub := &Hub{}
	hub.SetAccounting(stub)

	rec := callTop(t, hub,
		"/v1/platform/accounting/topn?dimension=tenant&tenant=t9&window=1h&limit=5")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if stub.last.TenantID != "t9" {
		t.Fatalf("expected the tenant filter to reach the store, got %q", stub.last.TenantID)
	}
	if stub.last.Limit != 5 {
		t.Fatalf("expected limit 5, got %d", stub.last.Limit)
	}
}

// TestAccountingUnavailable verifies 503 when accounting is off.
func TestAccountingUnavailable(t *testing.T) {
	hub := &Hub{}
	rec := callTop(t, hub, "/v1/platform/accounting/topn")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

// TestAccountingTopNMaxLimitAccepted pins the boundary: the largest
// documented page size must pass, so the rejection above is a real limit
// and not an off-by-one.
func TestAccountingTopNMaxLimitAccepted(t *testing.T) {
	hub := &Hub{}
	hub.SetAccounting(&stubAccounting{})

	rec := callTop(t, hub, "/v1/platform/accounting/topn?limit=200")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 at the maximum limit, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"limit":200`) {
		t.Fatalf("response should echo the applied limit: %s", rec.Body.String())
	}
}
