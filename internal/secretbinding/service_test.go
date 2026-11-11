// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_test.go covers delivery frame assembly with in-memory fakes.

package secretbinding

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeStore is the in-memory binding store with just the behavior the
// service tests need.
type fakeStore struct {
	rows map[string]Binding
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[string]Binding{}}
}

func rowKey(tenantID, gatewayID, secretName string) string {
	return tenantID + "." + gatewayID + "." + secretName
}

func (f *fakeStore) Upsert(_ context.Context, tenantID, gatewayID string, change DesiredChange, at time.Time) (Binding, error) {
	b := Binding{
		TenantID: tenantID, GatewayID: gatewayID, SecretName: change.SecretName,
		SecretType: change.SecretType, TypeVersion: change.TypeVersion,
		Selectors: change.Selectors, Revision: 1, Status: StatusPending,
		CreatedAt: at, UpdatedAt: at,
	}
	if existing, ok := f.rows[rowKey(tenantID, gatewayID, change.SecretName)]; ok {
		b.Revision = existing.Revision + 1
	}
	f.rows[rowKey(tenantID, gatewayID, change.SecretName)] = b
	return b, nil
}

func (f *fakeStore) Withdraw(_ context.Context, tenantID, gatewayID, secretName string, at time.Time) (Binding, error) {
	b, ok := f.rows[rowKey(tenantID, gatewayID, secretName)]
	if !ok {
		return Binding{}, ErrNotFound
	}
	b.Revision++
	b.Status = StatusRemoving
	b.UpdatedAt = at
	f.rows[rowKey(tenantID, gatewayID, secretName)] = b
	return b, nil
}

func (f *fakeStore) ListByGateway(_ context.Context, tenantID, gatewayID string, _ bool) ([]Binding, error) {
	out := []Binding{}
	for _, b := range f.rows {
		if b.TenantID == tenantID && b.GatewayID == gatewayID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeStore) ListBySecret(_ context.Context, tenantID, secretName string) ([]Binding, error) {
	out := []Binding{}
	for _, b := range f.rows {
		if b.TenantID == tenantID && b.SecretName == secretName && b.Status != StatusRemoved {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeStore) ActiveGatewayIDs(_ context.Context, tenantID, secretName string) ([]string, error) {
	out := []string{}
	for _, b := range f.rows {
		if b.TenantID == tenantID && b.SecretName == secretName && b.Status != StatusRemoved {
			out = append(out, b.GatewayID)
		}
	}
	return out, nil
}

func (f *fakeStore) References(_ context.Context, tenantID, secretName string) ([]Reference, error) {
	out := []Reference{}
	for _, b := range f.rows {
		if b.TenantID == tenantID && b.SecretName == secretName && b.Status != StatusRemoved {
			out = append(out, Reference{
				GatewayID:   b.GatewayID,
				GatewayName: b.GatewayID,
				Status:      b.Status,
			})
		}
	}
	return out, nil
}

func (f *fakeStore) Get(_ context.Context, tenantID, gatewayID, secretName string) (Binding, error) {
	b, ok := f.rows[rowKey(tenantID, gatewayID, secretName)]
	if !ok {
		return Binding{}, ErrNotFound
	}
	return b, nil
}

func (f *fakeStore) MarkDispatched(context.Context, string, string, int64, time.Time) error {
	return nil
}

func (f *fakeStore) Report(_ context.Context, tenantID, gatewayID string, report Report, at time.Time) error {
	b, ok := f.rows[rowKey(tenantID, gatewayID, report.SecretName)]
	if !ok {
		return ErrNotFound
	}
	b.Status = report.Status
	b.Reason = report.Reason
	b.UpdatedAt = at
	f.rows[rowKey(tenantID, gatewayID, report.SecretName)] = b
	return nil
}

func (f *fakeStore) MarkRemoved(context.Context, string, string, string, time.Time) error {
	return nil
}

func (f *fakeStore) ClaimPendingForAlarm(context.Context, time.Time) ([]Binding, error) {
	return nil, nil
}

func (f *fakeStore) ClaimFailedForRaise(context.Context) ([]Binding, error) {
	return nil, nil
}

func (f *fakeStore) ClaimFailedForEscalation(context.Context, time.Time) ([]Binding, error) {
	return nil, nil
}

// fakeDocs returns stored documents.
type fakeDocs struct {
	docs map[string]map[string]any
}

func (f fakeDocs) ResolveDocuments(_ context.Context, _ string, names []string) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	for _, name := range names {
		if doc, ok := f.docs[name]; ok {
			out[name] = doc
		}
	}
	return out, nil
}

// fakePaths reports secret paths.
type fakePaths struct{ paths []string }

func (f fakePaths) SecretPaths(context.Context, string, string, string) ([]string, error) {
	return f.paths, nil
}

// fakePub captures frames with their subjects.
type fakePub struct {
	frames [][]byte
	subs   []string
}

func (f *fakePub) Publish(subject string, data []byte) error {
	f.subs = append(f.subs, subject)
	f.frames = append(f.frames, data)
	return nil
}

// desiredFrame returns the PushFrame published on the desired subject.
func (f *fakePub) desiredFrame(t *testing.T) PushFrame {
	t.Helper()
	for i, subject := range f.subs {
		if strings.HasPrefix(subject, SubjectDesiredPrefix) {
			var frame PushFrame
			if err := json.Unmarshal(f.frames[i], &frame); err != nil {
				t.Fatal(err)
			}
			return frame
		}
	}
	t.Fatal("desired frame not published")
	return PushFrame{}
}

func newTestService() (*Service, *fakeStore, *fakePub) {
	store := newFakeStore()
	pub := &fakePub{}
	svc := New(store, pub,
		fakeDocs{docs: map[string]map[string]any{
			"cam1": {"username": "admin", "password": "s3cret"},
		}},
		fakePaths{paths: []string{"password"}})
	return svc, store, pub
}

func TestApplyMasksSecretFieldsInFrame(t *testing.T) {
	svc, _, pub := newTestService()
	_, err := svc.Apply(context.Background(), "t1", "gw1", DesiredChange{
		SecretName: "cam1", SecretType: "onvif-basic",
		Selectors: []Selector{{Kind: "mac", Value: "aa:bb"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	frame := pub.desiredFrame(t)
	if len(frame.Entries) != 1 {
		t.Fatal(frame.Entries)
	}
	doc := frame.Entries[0].Document
	if doc["password"] != "secret://cam1#password" {
		t.Fatalf("password = %v", doc["password"])
	}
	if doc["username"] != "admin" {
		t.Fatal("username should pass through")
	}
	if len(frame.Entries[0].Selectors) != 1 {
		t.Fatal("selectors missing")
	}
}

func TestApplyValidationErrors(t *testing.T) {
	svc, _, _ := newTestService()
	if _, err := svc.Apply(context.Background(), "t1", "gw1",
		DesiredChange{}); !errors.Is(err, ErrEmptySecret) {
		t.Fatalf("err = %v", err)
	}
	if _, err := svc.Apply(context.Background(), "t1", "gw1",
		DesiredChange{
			SecretName: "cam1",
			Selectors:  []Selector{{Kind: "mac"}},
		}); !errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("err = %v", err)
	}
}

func TestWithdrawAndRedrive(t *testing.T) {
	svc, _, pub := newTestService()
	if _, err := svc.Apply(context.Background(), "t1", "gw1", DesiredChange{SecretName: "cam1"}); err != nil {
		t.Fatal(err)
	}
	pub.frames = nil
	pub.subs = nil

	if err := svc.Withdraw(context.Background(), "t1", "gw1", "cam1"); err != nil {
		t.Fatal(err)
	}
	frame := pub.desiredFrame(t)
	if !frame.Entries[0].Removing {
		t.Fatal("entry should be removing")
	}
}

func TestReportStored(t *testing.T) {
	svc, store, _ := newTestService()
	if _, err := svc.Apply(context.Background(), "t1", "gw1", DesiredChange{SecretName: "cam1"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Report(context.Background(), "t1", "gw1", Report{
		SecretName: "cam1", Status: StatusApplied,
	}); err != nil {
		t.Fatal(err)
	}
	b := store.rows[rowKey("t1", "gw1", "cam1")]
	if b.Status != StatusApplied {
		t.Fatalf("status = %s", b.Status)
	}
}
