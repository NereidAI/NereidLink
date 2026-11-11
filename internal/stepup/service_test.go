// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_test.go covers the step-up lifecycle and signed payload handling
// with an in-memory store and a fake TOTP verifier, exercising every guard
// branch without a database.

package stepup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeStore keeps rows in a map keyed by challenge id.
type fakeStore struct {
	rows map[string]Challenge
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[string]Challenge{}}
}

func (f *fakeStore) Insert(_ context.Context, c Challenge) error {
	if _, exists := f.rows[c.ID]; exists {
		return errors.New("duplicate")
	}
	f.rows[c.ID] = c
	return nil
}

func (f *fakeStore) Get(_ context.Context, tenantID, userID, id string) (Challenge, error) {
	c, ok := f.rows[id]
	if !ok || c.TenantID != tenantID || c.UserID != userID {
		return Challenge{}, ErrNotFound
	}
	return c, nil
}

func (f *fakeStore) MarkVerified(_ context.Context, tenantID, userID, id string, at time.Time) (Challenge, error) {
	c, err := f.Get(context.Background(), tenantID, userID, id)
	if err != nil {
		return Challenge{}, err
	}
	if !c.ExpiresAt.After(at) {
		return c, ErrExpired
	}
	if c.Status != StatusPending {
		return c, ErrNotPending
	}
	c.Status = StatusVerified
	c.VerifiedAt = &at
	c.UpdatedAt = at
	f.rows[id] = c
	return c, nil
}

func (f *fakeStore) Consume(_ context.Context, tenantID, userID, id string, at time.Time) (Challenge, error) {
	c, err := f.Get(context.Background(), tenantID, userID, id)
	if err != nil {
		return Challenge{}, err
	}
	if !c.ExpiresAt.After(at) {
		return c, ErrExpired
	}
	if c.Status == StatusConsumed {
		return c, ErrReplay
	}
	if c.Status != StatusVerified {
		return c, ErrNotVerified
	}
	c.Status = StatusConsumed
	c.ConsumedAt = &at
	c.UpdatedAt = at
	f.rows[id] = c
	return c, nil
}

func (f *fakeStore) DeleteExpired(_ context.Context, before time.Time) error {
	for id, c := range f.rows {
		if !c.ExpiresAt.After(before) {
			delete(f.rows, id)
		}
	}
	return nil
}

// fakeVerifier accepts only the fixed good code.
type fakeVerifier struct {
	err error
}

func (v fakeVerifier) VerifyTOTP(_ context.Context, _ string, code string) error {
	if v.err != nil {
		return v.err
	}
	if code == "123456" {
		return nil
	}
	return errors.New("invalid code")
}

func newTestService(verifier TOTPVerifier) (*Service, *fakeStore) {
	store := newFakeStore()
	return New(store, verifier, []byte("test-signing-key")), store
}

func TestStartCreatesPendingSignedChallenge(t *testing.T) {
	svc, store := newTestService(fakeVerifier{})
	challenge, err := svc.Start(context.Background(), StartInput{
		TenantID: "t1", UserID: "u1", Action: "step_up:demo",
		Details: map[string]any{"gateway": "gw-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if challenge.Status != StatusPending {
		t.Fatalf("status = %q", challenge.Status)
	}
	if !strings.HasPrefix(challenge.SignedPayload, "nl-stepup://v1?") {
		t.Fatalf("payload = %q", challenge.SignedPayload)
	}
	if stored := store.rows[challenge.ID]; stored.DetailsSHA256 == "" {
		t.Fatal("details hash not stored")
	}
}

func TestStartRequiresAction(t *testing.T) {
	svc, _ := newTestService(fakeVerifier{})
	if _, err := svc.Start(context.Background(), StartInput{
		TenantID: "t1", UserID: "u1",
	}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCompleteHappyPathThenConsume(t *testing.T) {
	svc, _ := newTestService(fakeVerifier{})
	challenge, err := svc.Start(context.Background(), StartInput{
		TenantID: "t1", UserID: "u1", Action: "step_up:demo",
	})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := svc.Complete(context.Background(), "t1", "u1", challenge.ID, "123456", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if verified.Status != StatusVerified {
		t.Fatalf("status = %q", verified.Status)
	}
	if err := svc.Consume(context.Background(), "t1", "u1", challenge.ID, "step_up:demo", "ip"); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteWrongCodeDoesNotAdvance(t *testing.T) {
	svc, _ := newTestService(fakeVerifier{})
	challenge, _ := svc.Start(context.Background(), StartInput{
		TenantID: "t1", UserID: "u1", Action: "step_up:demo",
	})
	if _, err := svc.Complete(context.Background(), "t1", "u1", challenge.ID, "000000", "ip"); err == nil {
		t.Fatal("expected code failure")
	}
	if err := svc.Consume(context.Background(), "t1", "u1", challenge.ID, "step_up:demo", "ip"); !errors.Is(err, ErrNotVerified) {
		t.Fatalf("err = %v", err)
	}
}

func TestConsumeReplayIsRejected(t *testing.T) {
	svc, _ := newTestService(fakeVerifier{})
	challenge, _ := svc.Start(context.Background(), StartInput{
		TenantID: "t1", UserID: "u1", Action: "step_up:demo",
	})
	if _, err := svc.Complete(context.Background(), "t1", "u1", challenge.ID, "123456", "ip"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Consume(context.Background(), "t1", "u1", challenge.ID, "step_up:demo", "ip"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Consume(context.Background(), "t1", "u1", challenge.ID, "step_up:demo", "ip"); !errors.Is(err, ErrReplay) {
		t.Fatalf("err = %v", err)
	}
}

func TestConsumeActionMismatch(t *testing.T) {
	svc, _ := newTestService(fakeVerifier{})
	challenge, _ := svc.Start(context.Background(), StartInput{
		TenantID: "t1", UserID: "u1", Action: "step_up:demo",
	})
	if _, err := svc.Complete(context.Background(), "t1", "u1", challenge.ID, "123456", "ip"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Consume(context.Background(), "t1", "u1", challenge.ID, "other:action", "ip"); !errors.Is(err, ErrActionMismatch) {
		t.Fatalf("err = %v", err)
	}
}

func TestExpiredChallengeRejected(t *testing.T) {
	svc, store := newTestService(fakeVerifier{})
	challenge, _ := svc.Start(context.Background(), StartInput{
		TenantID: "t1", UserID: "u1", Action: "step_up:demo",
	})
	row := store.rows[challenge.ID]
	row.ExpiresAt = time.Now().Add(-time.Second)
	store.rows[challenge.ID] = row

	if _, err := svc.Complete(context.Background(), "t1", "u1", challenge.ID, "123456", "ip"); !errors.Is(err, ErrExpired) {
		t.Fatalf("err = %v", err)
	}
}

func TestConsumeUnknownChallenge(t *testing.T) {
	svc, _ := newTestService(fakeVerifier{})
	if err := svc.Consume(context.Background(), "t1", "u1", "missing", "step_up:demo", "ip"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestSweepDeletesExpiredRows(t *testing.T) {
	svc, store := newTestService(fakeVerifier{})
	challenge, _ := svc.Start(context.Background(), StartInput{
		TenantID: "t1", UserID: "u1", Action: "step_up:demo",
	})
	row := store.rows[challenge.ID]
	row.ExpiresAt = time.Now().Add(-time.Minute)
	store.rows[challenge.ID] = row

	if err := svc.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.rows[challenge.ID]; ok {
		t.Fatal("expired row not swept")
	}
}

func TestHashDetailsStableAcrossMapOrder(t *testing.T) {
	a, err := hashDetails(map[string]any{"b": "2", "a": "1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := hashDetails(map[string]any{"a": "1", "b": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("%s != %s", a, b)
	}
}

func TestSignedPayloadTamperEvident(t *testing.T) {
	key := []byte("k")
	exp := time.Now().Add(5 * time.Minute)
	payload := signPayload(key, "step_up:demo", "deadbeef", exp)
	other := signPayload([]byte("other"), "step_up:demo", "deadbeef", exp)
	if payload == other {
		t.Fatal("payloads for different keys must differ")
	}
	if signPayload(key, "step_up:demo", "deadbeef", exp.Add(time.Second)) == payload {
		t.Fatal("expiry change must change payload")
	}
}
