// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_test.go covers the opinvoke orchestration with in-memory fakes:
// catalog and size gating, immediate offline failure, synchronous success
// and failure, timeout mapping and persistence of every terminal state.

package opinvoke

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nereidai/nereidlink/internal/opcatalog"
	"github.com/nereidai/nereidlink/internal/presence"
)

// fakeStore is an in-memory task store.
type fakeStore struct {
	rows      map[string]Task
	completed map[string]string
	cutoff    time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		rows:      map[string]Task{},
		completed: map[string]string{},
	}
}

func (f *fakeStore) Insert(_ context.Context, t Task, _ time.Time) error {
	f.rows[t.ID] = t
	return nil
}

func (f *fakeStore) Complete(_ context.Context, id, status string, _ []byte, _ string, _ time.Time) error {
	f.completed[id] = status
	return nil
}

func (f *fakeStore) Get(_ context.Context, _, id string) (Task, error) {
	t, ok := f.rows[id]
	if !ok {
		return Task{}, ErrNotFound
	}
	return t, nil
}

func (f *fakeStore) ListByGateway(_ context.Context, _, _ string, limit int) ([]Task, error) {
	out := []Task{}
	for _, t := range f.rows {
		out = append(out, t)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) DeleteOlderThan(_ context.Context, cutoff time.Time) (int64, error) {
	f.cutoff = cutoff
	return 0, nil
}

// fakeCatalog maps operation keys to views.
type fakeCatalog struct {
	views       map[string]opcatalog.View
	rejectParam bool
}

func (f *fakeCatalog) EffectiveForGateway(_ context.Context, _, _, operation string) (opcatalog.View, bool, error) {
	v, ok := f.views[operation]
	return v, ok, nil
}

// ValidateParams optionally simulates a schema rejection; an empty-schema
// descriptor never reaches a failure.
func (f *fakeCatalog) ValidateParams(_ json.RawMessage, _ map[string]any) error {
	if f.rejectParam {
		return opcatalog.ErrInvalidParams
	}
	return nil
}

// fakeLocator decides gateway presence.
type fakeLocator struct {
	found bool
}

func (f *fakeLocator) Lookup(_ context.Context, _ presence.Identity) (presence.Location, error) {
	if !f.found {
		return presence.Location{}, nil
	}
	return presence.Location{Found: true, IngressID: "ing1"}, nil
}

// fakeRequester returns a programmed result and records the request.
type fakeRequester struct {
	reply    []byte
	err      error
	subject  string
	frame    InvokeFrame
	deadline time.Time
}

func (f *fakeRequester) RequestMsg(ctx context.Context, subject string, data []byte) ([]byte, error) {
	f.subject = subject
	_ = json.Unmarshal(data, &f.frame)
	if d, ok := ctx.Deadline(); ok {
		f.deadline = d
	}
	return f.reply, f.err
}

func newService(store taskStore, catalog CatalogLookup, locator GatewayLocator, req requester) *Service {
	return &Service{
		req: req, store: store, catalog: catalog, locator: locator,
	}
}

func testInput(operation string) InvokeInput {
	return InvokeInput{
		TenantID:  "t1",
		GatewayID: "gw1",
		Operation: operation,
		Params:    map[string]any{"delay": 1},
		Actor:     Actor{UserID: "u1", Email: "ops@example.com"},
	}
}

func readView(operation, risk string) opcatalog.View {
	return opcatalog.View{
		Descriptor: opcatalog.Descriptor{
			Operation: operation,
			Risk:      risk,
			TimeoutMs: 5000,
		},
	}
}

// TestInvokeSucceeded proves a successful round trip: the frame is sent to
// the located ingress and the edge result is stored as SUCCEEDED.
func TestInvokeSucceeded(t *testing.T) {
	store := newFakeStore()
	req := &fakeRequester{
		reply: mustMarshal(ResultFrame{
			ID: "ignored", OK: true,
			Result: json.RawMessage(`{"rtt_ms": 12}`),
		}),
	}
	svc := newService(store,
		&fakeCatalog{views: map[string]opcatalog.View{"ping": readView("ping", opcatalog.RiskRead)}},
		&fakeLocator{found: true}, req)

	task, err := svc.Invoke(context.Background(), testInput("ping"))
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StatusSucceeded {
		t.Fatalf("status=%s want SUCCEEDED", task.Status)
	}
	if string(task.Result) != `{"rtt_ms":12}` {
		t.Fatalf("result not stored: %s", task.Result)
	}
	if req.subject != DispatchSubjectPrefix+"ing1" {
		t.Fatalf("dispatched to wrong subject: %s", req.subject)
	}
	if store.completed[task.ID] != StatusSucceeded {
		t.Fatal("terminal status not persisted")
	}
}

// TestInvokeOfflineNeverQueues verifies an absent presence fails
// immediately with NOT_CONNECTED and never reaches the request transport.
func TestInvokeOfflineNeverQueues(t *testing.T) {
	store := newFakeStore()
	req := &fakeRequester{err: errors.New("must not be called")}
	svc := newService(store,
		&fakeCatalog{views: map[string]opcatalog.View{"ping": readView("ping", opcatalog.RiskRead)}},
		&fakeLocator{found: false}, req)

	task, err := svc.Invoke(context.Background(), testInput("ping"))
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StatusNotConnected {
		t.Fatalf("status=%s want NOT_CONNECTED", task.Status)
	}
	if store.completed[task.ID] != StatusNotConnected {
		t.Fatal("NOT_CONNECTED must be persisted")
	}
}

// TestInvokeOperationMissingAndDisabled covers catalog gating.
func TestInvokeOperationMissingAndDisabled(t *testing.T) {
	svc := newService(newFakeStore(), &fakeCatalog{views: map[string]opcatalog.View{}},
		&fakeLocator{found: true}, &fakeRequester{})

	if _, err := svc.Invoke(context.Background(), testInput("ghost")); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("want ErrOperationNotFound, got %v", err)
	}

	disabled := readView("reboot", opcatalog.RiskDangerous)
	disabled.Disabled = true
	svc = newService(newFakeStore(),
		&fakeCatalog{views: map[string]opcatalog.View{"reboot": disabled}},
		&fakeLocator{found: true}, &fakeRequester{})
	if _, err := svc.Invoke(context.Background(), testInput("reboot")); !errors.Is(err, ErrOperationDisabled) {
		t.Fatalf("want ErrOperationDisabled, got %v", err)
	}
}

// TestInvokeEdgeFailed maps an edge failure to a stored FAILED task.
func TestInvokeEdgeFailed(t *testing.T) {
	req := &fakeRequester{reply: mustMarshal(ResultFrame{
		OK: false, Error: "unit busy",
	})}
	svc := newService(newFakeStore(),
		&fakeCatalog{views: map[string]opcatalog.View{"ping": readView("ping", opcatalog.RiskRead)}},
		&fakeLocator{found: true}, req)

	task, err := svc.Invoke(context.Background(), testInput("ping"))
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StatusFailed || task.Reason != "unit busy" {
		t.Fatalf("unexpected task: %+v", task)
	}
}

// TestInvokeTimeout maps a transport deadline to TIMEOUT.
func TestInvokeTimeout(t *testing.T) {
	req := &fakeRequester{err: context.DeadlineExceeded}
	svc := newService(newFakeStore(),
		&fakeCatalog{views: map[string]opcatalog.View{"ping": readView("ping", opcatalog.RiskRead)}},
		&fakeLocator{found: true}, req)

	task, err := svc.Invoke(context.Background(), testInput("ping"))
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StatusTimeout {
		t.Fatalf("status=%s want TIMEOUT", task.Status)
	}
}

// TestInvokeParamsTooLarge enforces the 64KB params boundary before the
// task is inserted.
func TestInvokeParamsTooLarge(t *testing.T) {
	store := newFakeStore()
	svc := newService(store,
		&fakeCatalog{views: map[string]opcatalog.View{"ping": readView("ping", opcatalog.RiskRead)}},
		&fakeLocator{found: true}, &fakeRequester{})

	in := testInput("ping")
	in.Params = map[string]any{"blob": strings.Repeat("x", MaxParamsBytes+1)}
	if _, err := svc.Invoke(context.Background(), in); !errors.Is(err, ErrParamsTooLarge) {
		t.Fatalf("want ErrParamsTooLarge, got %v", err)
	}
	if len(store.rows) != 0 {
		t.Fatal("oversized params must not be persisted")
	}
}

// TestClampTimeout checks the hard timeout range mapping.
func TestClampTimeout(t *testing.T) {
	if got := clampTimeout(0); got != MaxWaitMs {
		t.Fatalf("missing timeout must default to cap, got %d", got)
	}
	if got := clampTimeout(MaxWaitMs + 1); got != MaxWaitMs {
		t.Fatalf("timeout must clamp to cap, got %d", got)
	}
	if got := clampTimeout(MinWaitMs - 1); got != MinWaitMs {
		t.Fatalf("timeout must clamp to floor, got %d", got)
	}
	if got := clampTimeout(100); got != 100 {
		t.Fatalf("valid timeout must pass, got %d", got)
	}
}

// TestInvokePerCallTimeoutCanOnlyLower verifies the operator-supplied
// timeout shortens the descriptor default but never raises it, and both are
// bounded by the 60s cap / 100ms floor.
func TestInvokePerCallTimeoutCanOnlyLower(t *testing.T) {
	reply := mustMarshal(ResultFrame{OK: true, Result: json.RawMessage(`{}`)})

	cases := []struct {
		name     string
		override int
		want     int
	}{
		{"default when zero", 0, 5000},
		{"lowered", 2000, 2000},
		{"override cannot raise", 30000, 5000},
		{"below floor clamps to floor", 1, MinWaitMs},
		{"above cap clamps to cap but still cannot raise", 120000, 5000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &fakeRequester{reply: reply}
			svc := newService(newFakeStore(),
				&fakeCatalog{views: map[string]opcatalog.View{"ping": readView("ping", opcatalog.RiskRead)}},
				&fakeLocator{found: true}, req)
			in := testInput("ping")
			in.TimeoutMs = tc.override
			if _, err := svc.Invoke(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			if req.frame.TimeoutMs != tc.want {
				t.Fatalf("frame timeout=%d want %d", req.frame.TimeoutMs, tc.want)
			}
		})
	}
}

// TestInvokeParamsRejectedBySchema verifies params failing the descriptor
// schema are rejected before a task is inserted or dispatched.
func TestInvokeParamsRejectedBySchema(t *testing.T) {
	store := newFakeStore()
	req := &fakeRequester{err: errors.New("must not be called")}
	catalog := &fakeCatalog{
		views:       map[string]opcatalog.View{"ping": readView("ping", opcatalog.RiskRead)},
		rejectParam: true,
	}
	svc := newService(store, catalog, &fakeLocator{found: true}, req)

	_, err := svc.Invoke(context.Background(), testInput("ping"))
	if !errors.Is(err, ErrParamsSchema) {
		t.Fatalf("want ErrParamsSchema, got %v", err)
	}
	if len(store.rows) != 0 {
		t.Fatal("schema-invalid params must not be persisted")
	}
	if req.subject != "" {
		t.Fatal("schema-invalid params must not be dispatched")
	}
}

// TestInvokeResultTooLarge verifies an edge result past the 64KB boundary
// is recorded FAILED rather than returned.
func TestInvokeResultTooLarge(t *testing.T) {
	oversized := append(
		[]byte(`"`),
		append([]byte(strings.Repeat("x", MaxResultBytes+1)), '"')...)
	req := &fakeRequester{reply: mustMarshal(ResultFrame{
		OK: true, Result: oversized,
	})}
	svc := newService(newFakeStore(),
		&fakeCatalog{views: map[string]opcatalog.View{"ping": readView("ping", opcatalog.RiskRead)}},
		&fakeLocator{found: true}, req)

	task, err := svc.Invoke(context.Background(), testInput("ping"))
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StatusFailed {
		t.Fatalf("status=%s want FAILED", task.Status)
	}
}

// TestInvokeOneWayAwait verifies one-way operations wait for the fixed
// ingress acknowledgment window, not the (shorter) descriptor timeout.
func TestInvokeOneWayAwait(t *testing.T) {
	view := readView("fire", opcatalog.RiskNormal)
	view.Interaction = opcatalog.InteractionOneWay
	view.TimeoutMs = 500
	req := &fakeRequester{reply: mustMarshal(ResultFrame{OK: true})}
	svc := newService(newFakeStore(),
		&fakeCatalog{views: map[string]opcatalog.View{"fire": view}},
		&fakeLocator{found: true}, req)

	start := time.Now()
	task, err := svc.Invoke(context.Background(), testInput("fire"))
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != StatusSucceeded {
		t.Fatalf("status=%s want SUCCEEDED", task.Status)
	}
	if !req.frame.OneWay {
		t.Fatal("frame must be marked one-way")
	}
	remaining := time.Until(req.deadline)
	want := oneWayAwait - time.Since(start)
	if remaining < want-500*time.Millisecond || remaining > want+500*time.Millisecond {
		t.Fatalf("one-way await=%v want ~%v", remaining, oneWayAwait)
	}
}

// TestSweepCutoff verifies the retention sweep deletes older than 90 days.
func TestSweepCutoff(t *testing.T) {
	store := newFakeStore()
	svc := newService(store,
		&fakeCatalog{views: map[string]opcatalog.View{}},
		&fakeLocator{found: false}, &fakeRequester{})

	if err := svc.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	age := time.Since(store.cutoff)
	if age < Retention-time.Minute || age > Retention+time.Minute {
		t.Fatalf("sweep cutoff age=%v want ~%v", age, Retention)
	}
}

// mustMarshal encodes a frame, failing the test on error.
func mustMarshal(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
