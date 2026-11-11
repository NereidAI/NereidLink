// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"sync"
	"time"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
)

// Session is one live device session on a request-based protocol. The
// device keeps one command request in flight; commands queue in mailbox
// until the next request drains them.
type Session struct {
	// identity names the device owning the session.
	identity SessionIdentity

	// sessionID is the fencing token returned by presence.Connect.
	sessionID string

	// mailbox buffers commands for the next device request.
	mailbox chan *v1.Command

	mu       sync.Mutex
	lastSeen time.Time
}

// SessionIdentity identifies the device owning a session.
type SessionIdentity struct {
	// TenantID owns the device.
	TenantID string

	// DeviceID is the device identifier.
	DeviceID string
}

// Identity returns the session identity.
func (s *Session) Identity() SessionIdentity {
	return s.identity
}

// SessionID returns the presence fencing token.
func (s *Session) SessionID() string {
	return s.sessionID
}

// Mailbox returns the command channel.
func (s *Session) Mailbox() <-chan *v1.Command {
	return s.mailbox
}

// LastSeen returns the latest request time.
func (s *Session) LastSeen() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSeen
}

// touch records a request at now.
func (s *Session) touch(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSeen = now
}

// Sessions tracks live device sessions and expires idle ones.
type Sessions struct {
	mailboxSize int

	mu    sync.Mutex
	items map[string]*Session
}

// NewSessions constructs a session registry with bounded mailboxes.
func NewSessions(mailboxSize int) *Sessions {
	if mailboxSize <= 0 {
		mailboxSize = 1
	}
	return &Sessions{
		mailboxSize: mailboxSize,
		items:       map[string]*Session{},
	}
}

// sessionKey is the per-device map key.
func sessionKey(tenantID, deviceID string) string {
	return tenantID + "." + deviceID
}

// Ensure returns the live session, creating one when absent. A new session
// is reported via the bool so the caller can run presence.Connect.
func (s *Sessions) Ensure(identity SessionIdentity, now time.Time) (*Session, bool) {
	key := sessionKey(identity.TenantID, identity.DeviceID)
	s.mu.Lock()
	defer s.mu.Unlock()

	if current, ok := s.items[key]; ok {
		current.touch(now)
		return current, false
	}
	current := &Session{
		identity: identity,
		mailbox:  make(chan *v1.Command, s.mailboxSize),
		lastSeen: now,
	}
	s.items[key] = current
	return current, true
}

// SetSessionID records the presence fencing token on a new session.
func (s *Sessions) SetSessionID(identity SessionIdentity, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.items[sessionKey(identity.TenantID, identity.DeviceID)]; ok {
		current.sessionID = sessionID
	}
}

// SessionFor resolves a device to its live session.
func (s *Sessions) SessionFor(tenantID, deviceID string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.items[sessionKey(tenantID, deviceID)]
	return current, ok
}

// Remove deletes a session, used on disconnect.
func (s *Sessions) Remove(identity SessionIdentity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, sessionKey(identity.TenantID, identity.DeviceID))
}

// Sweep removes idle sessions older than ttl and returns their identities
// so the caller can run presence.Disconnect with their fencing tokens.
func (s *Sessions) Sweep(now time.Time, ttl time.Duration) []SweptSession {
	s.mu.Lock()
	defer s.mu.Unlock()

	swept := []SweptSession{}
	for key, current := range s.items {
		current.mu.Lock()
		idle := now.Sub(current.lastSeen) > ttl
		current.mu.Unlock()
		if !idle {
			continue
		}
		swept = append(swept, SweptSession{
			Identity:  current.identity,
			SessionID: current.sessionID,
		})
		delete(s.items, key)
	}
	return swept
}

// SweptSession carries one expired session to its disconnect call.
type SweptSession struct {
	// Identity names the device.
	Identity SessionIdentity

	// SessionID is the fencing token for presence.Disconnect.
	SessionID string
}

// Run sweeps idle sessions on every interval until ctx is canceled.
// onSweep is invoked for each expired session, typically to disconnect
// presence; errors stop the loop.
func (s *Sessions) Run(ctx context.Context, interval, ttl time.Duration,
	onSweep func(context.Context, SweptSession) error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			for _, swept := range s.Sweep(time.Now().UTC(), ttl) {
				if err := onSweep(ctx, swept); err != nil {
					return err
				}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
