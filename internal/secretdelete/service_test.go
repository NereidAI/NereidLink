// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_test.go covers forced deletion orchestration with in-memory
// fakes: withdrawal of all references, deferred vault deletion, hook and
// sweep finalization.

package secretdelete

import (
	"context"
	"errors"
	"testing"

	"github.com/nereidai/nereidlink/internal/secretbinding"
)

// fakeStore is the in-memory pending store.
type fakeStore struct {
	rows map[string]PendingDelete
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[string]PendingDelete{}}
}

func key(tenantID, name string) string { return tenantID + "." + name }

func (f *fakeStore) Upsert(_ context.Context, p PendingDelete) error {
	if _, ok := f.rows[key(p.TenantID, p.SecretName)]; ok {
		return nil
	}
	f.rows[key(p.TenantID, p.SecretName)] = p
	return nil
}

func (f *fakeStore) List(_ context.Context) ([]PendingDelete, error) {
	out := []PendingDelete{}
	for _, p := range f.rows {
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeStore) Exists(_ context.Context, tenantID, name string) (bool, error) {
	_, ok := f.rows[key(tenantID, name)]
	return ok, nil
}

func (f *fakeStore) Delete(_ context.Context, tenantID, name string) error {
	delete(f.rows, key(tenantID, name))
	return nil
}

// fakeBindings is the in-memory binding admin.
type fakeBindings struct {
	refs map[string][]secretbinding.Reference
	// withdrawn records gateway ids withdrawn per secret.
	withdrawn map[string][]string
	// withdrawErr optionally forces an error on Withdraw.
	withdrawErr error
}

func newFakeBindings() *fakeBindings {
	return &fakeBindings{
		refs:      map[string][]secretbinding.Reference{},
		withdrawn: map[string][]string{},
	}
}

func (f *fakeBindings) References(_ context.Context, _ string, secretName string) ([]secretbinding.Reference, error) {
	return f.refs[secretName], nil
}

func (f *fakeBindings) Withdraw(_ context.Context, _ string, gatewayID, secretName string) error {
	if f.withdrawErr != nil {
		return f.withdrawErr
	}
	f.withdrawn[secretName] = append(f.withdrawn[secretName], gatewayID)
	return nil
}

// fakeVault records deleted secret names.
type fakeVault struct{ deleted []string }

func (f *fakeVault) Delete(_ context.Context, _ string, name string) error {
	f.deleted = append(f.deleted, name)
	return nil
}

func newTestService() (*Service, *fakeStore, *fakeBindings, *fakeVault) {
	st := newFakeStore()
	bi := newFakeBindings()
	va := &fakeVault{}
	return New(st, bi, va), st, bi, va
}

// TestForceDeleteWithdrawsAndDefersVaultDelete verifies every reference is
// withdrawn and the marker stored while the vault original survives.
func TestForceDeleteWithdrawsAndDefersVaultDelete(t *testing.T) {
	svc, st, bi, va := newTestService()
	bi.refs["cam1"] = []secretbinding.Reference{
		{GatewayID: "gw1", GatewayName: "gw-1", Status: secretbinding.StatusApplied},
		{GatewayID: "gw2", GatewayName: "gw-2", Status: secretbinding.StatusDispatched},
	}
	pending, err := svc.ForceDelete(context.Background(), "t1", "cam1", Actor{UserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if pending.SecretName != "cam1" {
		t.Fatalf("pending = %+v", pending)
	}
	if len(bi.withdrawn["cam1"]) != 2 {
		t.Fatalf("withdrawn = %v", bi.withdrawn)
	}
	if len(va.deleted) != 0 {
		t.Fatalf("vault deleted too early: %v", va.deleted)
	}
	if ok, _ := st.Exists(context.Background(), "t1", "cam1"); !ok {
		t.Fatal("pending marker missing")
	}
}

// TestForceDeleteFastPath verifies zero references hard-delete the vault
// original without a marker.
func TestForceDeleteFastPath(t *testing.T) {
	svc, st, _, va := newTestService()
	if _, err := svc.ForceDelete(context.Background(), "t1", "cam1", Actor{UserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if len(va.deleted) != 1 || va.deleted[0] != "cam1" {
		t.Fatalf("deleted = %v", va.deleted)
	}
	if ok, _ := st.Exists(context.Background(), "t1", "cam1"); ok {
		t.Fatal("marker should not exist on fast path")
	}
}

// TestFinalizeAfterRemovalHook verifies the binding removal hook deletes
// the vault original once references disappear.
func TestFinalizeAfterRemovalHook(t *testing.T) {
	svc, st, bi, va := newTestService()
	bi.refs["cam1"] = []secretbinding.Reference{
		{GatewayID: "gw1", Status: secretbinding.StatusRemoving},
	}
	if _, err := svc.ForceDelete(context.Background(), "t1", "cam1", Actor{UserID: "u1"}); err != nil {
		t.Fatal(err)
	}

	// Edge confirms removal: references become empty, hook fires.
	bi.refs["cam1"] = nil
	svc.OnRemoval(context.Background(), "t1", "cam1")

	if len(va.deleted) != 1 {
		t.Fatalf("vault not deleted: %v", va.deleted)
	}
	if ok, _ := st.Exists(context.Background(), "t1", "cam1"); ok {
		t.Fatal("marker not removed")
	}
}

// TestSweepFinalizes verifies the periodic sweep converges a pending row
// even without a hook notification: once references are empty the vault
// original and marker are removed.
func TestSweepFinalizes(t *testing.T) {
	svc, st, bi, va := newTestService()
	bi.refs["cam1"] = []secretbinding.Reference{
		{GatewayID: "gw1", Status: secretbinding.StatusRemoving},
	}
	if _, err := svc.ForceDelete(context.Background(), "t1", "cam1", Actor{UserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	bi.refs["cam1"] = nil
	svc.sweep(context.Background())
	if len(va.deleted) != 1 {
		t.Fatalf("vault not deleted by sweep: %v", va.deleted)
	}
	if ok, _ := st.Exists(context.Background(), "t1", "cam1"); ok {
		t.Fatal("marker not removed by sweep")
	}
}

// TestSweepReissuesMissingWithdrawal verifies an active (non-REMOVING)
// reference gets withdrawn again by the sweep.
func TestSweepReissuesMissingWithdrawal(t *testing.T) {
	svc, _, bi, _ := newTestService()
	bi.refs["cam1"] = []secretbinding.Reference{
		{GatewayID: "gw1", Status: secretbinding.StatusApplied},
	}
	if _, err := svc.ForceDelete(context.Background(), "t1", "cam1", Actor{UserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	bi.withdrawn = map[string][]string{}
	// Simulate the first withdrawal being lost (binding back to APPLIED).
	bi.refs["cam1"][0].Status = secretbinding.StatusApplied
	svc.sweep(context.Background())
	if len(bi.withdrawn["cam1"]) != 1 {
		t.Fatalf("sweep did not re-withdraw: %v", bi.withdrawn)
	}
}

// TestForceDeleteIgnoresConcurrentWithdrawal verifies a binding that
// already moved to REMOVING (ErrNotFound) does not fail the forced delete.
func TestForceDeleteIgnoresConcurrentWithdrawal(t *testing.T) {
	svc, _, bi, _ := newTestService()
	bi.refs["cam1"] = []secretbinding.Reference{{GatewayID: "gw1", Status: secretbinding.StatusRemoving}}
	bi.withdrawErr = secretbinding.ErrNotFound
	pending, err := svc.ForceDelete(context.Background(), "t1", "cam1", Actor{UserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if pending.SecretName != "cam1" {
		t.Fatal("marker not stored")
	}
}

// TestForceDeletePropagatesOtherErrors verifies a non-NotFound withdrawal
// error fails the operation.
func TestForceDeletePropagatesOtherErrors(t *testing.T) {
	svc, _, bi, _ := newTestService()
	bi.refs["cam1"] = []secretbinding.Reference{{GatewayID: "gw1", Status: secretbinding.StatusApplied}}
	bi.withdrawErr = errors.New("boom")
	if _, err := svc.ForceDelete(context.Background(), "t1", "cam1", Actor{UserID: "u1"}); err == nil {
		t.Fatal("expected error")
	}
}
