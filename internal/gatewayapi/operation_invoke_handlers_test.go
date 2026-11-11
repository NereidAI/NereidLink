// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// operation_invoke_handlers_test.go pins the ADR-0035 REST gate: machine
// principals are refused at every risk level, dangerous actions require a
// fresh step-up before any task is created, the per-call timeout reaches the
// invoker, and schema failures map to 400.

package gatewayapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nereidai/nereidlink/internal/opcatalog"
	"github.com/nereidai/nereidlink/internal/opinvoke"
	"github.com/nereidai/nereidlink/internal/rbac"
)

// stubOperationCatalog serves a fixed effective catalog.
type stubOperationCatalog struct {
	views []opcatalog.View
}

func (s *stubOperationCatalog) Effective(_ context.Context, _ string) ([]opcatalog.View, error) {
	return s.views, nil
}
func (s *stubOperationCatalog) ListForGateway(_ context.Context, _, _ string) ([]opcatalog.View, error) {
	return s.views, nil
}
func (s *stubOperationCatalog) EffectiveForGateway(_ context.Context, _, _, operation string) (opcatalog.View, bool, error) {
	for _, v := range s.views {
		if v.Operation == operation {
			return v, true, nil
		}
	}
	return opcatalog.View{}, false, nil
}
func (s *stubOperationCatalog) CreateManual(context.Context, string, opcatalog.Descriptor) error {
	return nil
}
func (s *stubOperationCatalog) UpdateManual(context.Context, string, string, string, opcatalog.Descriptor) error {
	return nil
}
func (s *stubOperationCatalog) DeleteManual(context.Context, string, string, string) error {
	return nil
}
func (s *stubOperationCatalog) CloneToPrivate(context.Context, string, string, string, string) error {
	return nil
}
func (s *stubOperationCatalog) SetDisabled(context.Context, string, string, string, string) error {
	return nil
}
func (s *stubOperationCatalog) ClearDisabled(context.Context, string, string, string) error {
	return nil
}

// stubOperationInvoker records the last input and returns a programmed task/error.
type stubOperationInvoker struct {
	last opinvoke.InvokeInput
	task opinvoke.Task
	err  error
}

func (s *stubOperationInvoker) Invoke(_ context.Context, in opinvoke.InvokeInput) (opinvoke.Task, error) {
	s.last = in
	return s.task, s.err
}
func (s *stubOperationInvoker) Tasks(context.Context, string, string, int) ([]opinvoke.Task, error) {
	return nil, nil
}
func (s *stubOperationInvoker) GetTask(context.Context, string, string) (opinvoke.Task, error) {
	return opinvoke.Task{}, nil
}

// newInvokeRequest builds a POST with path values, principal and JSON body.
func newInvokeRequest(principal *rbac.Principal, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost,
		"/v1/tenants/t1/gateways/gw1/operations/reboot/invoke",
		strings.NewReader(body))
	req.SetPathValue("tenant", "t1")
	req.SetPathValue("gateway", "gw1")
	req.SetPathValue("operation", "reboot")
	if principal != nil {
		req = req.WithContext(context.WithValue(req.Context(),
			principalContext{}, principal))
	}
	return req
}

// invokeHub wires a catalog + invoker onto a bare Hub.
func invokeHub(views ...opcatalog.View) (*Hub, *stubOperationInvoker) {
	hub := &Hub{}
	hub.SetOperationCatalog(&stubOperationCatalog{views: views})
	invoker := &stubOperationInvoker{task: opinvoke.Task{Status: opinvoke.StatusSucceeded}}
	hub.SetOperationInvoker(invoker)
	return hub, invoker
}

func opView(operation, risk string) opcatalog.View {
	return opcatalog.View{
		Descriptor: opcatalog.Descriptor{Operation: operation, Risk: risk, TimeoutMs: 5000},
	}
}

func humanPrincipal() *rbac.Principal {
	return &rbac.Principal{UserID: "u1", Email: "ops@example.com"}
}

// TestOperationInvokeMachineRefusedAtEveryRisk verifies the human-only gate
// for read and normal as well as dangerous, before the invoker is touched.
func TestOperationInvokeMachineRefusedAtEveryRisk(t *testing.T) {
	for _, risk := range []string{opcatalog.RiskRead, opcatalog.RiskNormal, opcatalog.RiskDangerous} {
		hub, invoker := invokeHub(opView("reboot", risk))
		rec := httptest.NewRecorder()
		machine := &rbac.Principal{UserID: "svc", Machine: true}
		hub.handleOperationInvoke(rec, newInvokeRequest(machine, `{"params":{}}`))

		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: expected 403, got %d (%s)", risk, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "OPERATION_HUMAN_ONLY") {
			t.Fatalf("%s: expected OPERATION_HUMAN_ONLY, got %s", risk, rec.Body.String())
		}
		if invoker.last.Operation != "" {
			t.Fatalf("%s: machine invoke must never reach the invoker", risk)
		}
	}
}

// TestOperationInvokeNilPrincipalRefused covers an unauthenticated caller.
func TestOperationInvokeNilPrincipalRefused(t *testing.T) {
	hub, invoker := invokeHub(opView("reboot", opcatalog.RiskRead))
	rec := httptest.NewRecorder()
	hub.handleOperationInvoke(rec, newInvokeRequest(nil, `{"params":{}}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if invoker.last.Operation != "" {
		t.Fatal("anonymous invoke must never reach the invoker")
	}
}

// TestOperationInvokeReadExecutes verifies a human read call passes through.
func TestOperationInvokeReadExecutes(t *testing.T) {
	hub, invoker := invokeHub(opView("reboot", opcatalog.RiskRead))
	rec := httptest.NewRecorder()
	hub.handleOperationInvoke(rec,
		newInvokeRequest(humanPrincipal(), `{"params":{"delay":1},"timeout_ms":2000}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if invoker.last.Operation != "reboot" {
		t.Fatalf("expected invoke to reach invoker, got %+v", invoker.last)
	}
	if invoker.last.TimeoutMs != 2000 {
		t.Fatalf("timeout override not forwarded: %d", invoker.last.TimeoutMs)
	}
	if invoker.last.Actor.UserID != "u1" {
		t.Fatalf("actor not forwarded: %+v", invoker.last.Actor)
	}
}

// TestOperationInvokeDangerousStepUpRequired verifies 428 without a
// step_up_id and that no task is created.
func TestOperationInvokeDangerousStepUpRequired(t *testing.T) {
	hub, invoker := invokeHub(opView("reboot", opcatalog.RiskDangerous))
	rec := httptest.NewRecorder()
	hub.handleOperationInvoke(rec, newInvokeRequest(humanPrincipal(), `{"params":{}}`))
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("expected 428, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "STEP_UP_REQUIRED") {
		t.Fatalf("expected STEP_UP_REQUIRED, got %s", rec.Body.String())
	}
	if invoker.last.Operation != "" {
		t.Fatal("dangerous invoke without step-up must not reach invoker")
	}
}

// TestOperationInvokeStepUpUnavailable verifies 503 when step-up is presented
// but the service is not wired.
func TestOperationInvokeStepUpUnavailable(t *testing.T) {
	hub, _ := invokeHub(opView("reboot", opcatalog.RiskDangerous))
	rec := httptest.NewRecorder()
	hub.handleOperationInvoke(rec,
		newInvokeRequest(humanPrincipal(), `{"params":{},"step_up_id":"abc"}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "STEP_UP_UNAVAILABLE") {
		t.Fatalf("expected STEP_UP_UNAVAILABLE, got %s", rec.Body.String())
	}
}

// TestOperationInvokeNotFound verifies catalog gating at the handler.
func TestOperationInvokeNotFound(t *testing.T) {
	hub, _ := invokeHub() // empty catalog
	rec := httptest.NewRecorder()
	hub.handleOperationInvoke(rec, newInvokeRequest(humanPrincipal(), `{"params":{}}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestOperationInvokeParamsInvalidMaps400 verifies the schema error mapping.
func TestOperationInvokeParamsInvalidMaps400(t *testing.T) {
	hub, invoker := invokeHub(opView("reboot", opcatalog.RiskRead))
	invoker.err = opinvoke.ErrParamsSchema
	rec := httptest.NewRecorder()
	hub.handleOperationInvoke(rec, newInvokeRequest(humanPrincipal(), `{"params":{}}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "OPERATION_PARAMS_INVALID") {
		t.Fatalf("expected OPERATION_PARAMS_INVALID, got %s", rec.Body.String())
	}
}
