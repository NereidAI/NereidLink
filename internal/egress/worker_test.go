// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package egress

import (
	"context"
	"testing"
	"time"
)

// fakeWorkerStore returns a fixed forwarder set so reconcile can run
// without a database.
type fakeWorkerStore struct {
	all []Forwarder
}

func (f *fakeWorkerStore) ListAll(context.Context) ([]Forwarder, error) {
	return f.all, nil
}

func (f *fakeWorkerStore) InsertDLQ(context.Context, *DeadLetter) error {
	return nil
}

func (f *fakeWorkerStore) InsertDelivery(context.Context, *Delivery) error {
	return nil
}

func (f *fakeWorkerStore) PruneDeliveries(context.Context, time.Time, int) error {
	return nil
}

// newDeadlockWorker builds a worker whose consumer start always fails
// (nil JetStream), exercising the path where start is invoked for an
// enabled forwarder.
func newDeadlockWorker(forwarders ...Forwarder) *Worker {
	return &Worker{
		store:   &fakeWorkerStore{all: forwarders},
		running: map[string]*runner{},
	}
}

// TestReconcileWithEnabledForwarderReturns is the regression guard for
// the self-deadlock where reconcile held w.mu while calling start, which
// re-locks the same non-reentrant mutex: reconcile must return instead of
// blocking forever even when the consumer cannot start.
func TestReconcileWithEnabledForwarderReturns(t *testing.T) {
	now := time.Now().UTC()
	w := newDeadlockWorker(Forwarder{
		ID: "fwd-1", TenantID: "tenant-1", Enabled: true, UpdatedAt: now,
	})

	done := make(chan error, 1)
	go func() { done <- w.reconcile(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("reconcile must not fail on a failed consumer start: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconcile deadlocked: start was called while w.mu was held")
	}

	// A second cycle with the forwarder gone must also return and try to
	// delete its stream; the worker stays responsive.
	w.store = &fakeWorkerStore{}
	go func() { done <- w.reconcile(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second reconcile must not fail: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second reconcile deadlocked")
	}
}

// TestRunStopsOnContextCancel proves Run returns promptly after cancel
// once reconcile cannot wedge it.
func TestRunStopsOnContextCancel(t *testing.T) {
	w := newDeadlockWorker(Forwarder{
		ID: "fwd-1", TenantID: "tenant-1", Enabled: true, UpdatedAt: time.Now().UTC(),
	})
	ctx, cancel := context.WithCancel(context.Background())

	result := make(chan error, 1)
	go func() { result <- w.Run(ctx) }()

	// Let the initial reconcile run, then cancel.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run must return nil after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop: shutdown blocked by the reconcile deadlock")
	}
}
