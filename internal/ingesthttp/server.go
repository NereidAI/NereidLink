// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package ingesthttp implements the southbound HTTP device ingress.
//
// It exposes the same capabilities as the MQTT ingress over three
// device-facing endpoints:
//
//   - POST /v1/me/telemetry      uplink JSON telemetry frames.
//   - GET  /v1/me/commands        long-poll retrieval of one command.
//   - POST /v1/me/cmdres/{id}     command response uplink.
//
// Authentication accepts two schemes: "Authorization: Bearer
// <token>" for passwordless access tokens (credentials_value empty,
// the seed/compat shape) and "Authorization: Basic base64(id:secret)"
// so credentials carrying a secret can also authenticate over HTTP,
// mirroring the MQTT username/password pair. The first authenticated
// request of a device opens a presence session; the session ends
// through an idle timeout because HTTP has no explicit disconnect
// event.
package ingesthttp

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/internal/accounting"
	"github.com/nereidai/nereidlink/internal/presence"
	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"
)

// Authenticator verifies presented access tokens. The registry gRPC
// client satisfies it.
type Authenticator interface {
	// Authenticate validates the token and returns the device identity.
	Authenticate(ctx context.Context, username, password string) (sdktelemetry.DeviceIdentity, bool, error)
}

// PresenceManager starts and ends presence sessions. *presence.Service
// satisfies it.
type PresenceManager interface {
	// Connect registers the session and returns its fencing session ID.
	Connect(ctx context.Context, id presence.Identity, ingressID, gatewayID string) (string, error)

	// Disconnect ends the session only when sessionID still fences it.
	Disconnect(ctx context.Context, id presence.Identity, sessionID string) error
}

// Tuning configures long-poll and session lifecycle behavior.
type Tuning struct {
	// LongPollWait bounds one GET commands request before an empty reply.
	LongPollWait time.Duration

	// SessionTTL bounds how long a session stays online after the last
	// authenticated request.
	SessionTTL time.Duration

	// SweepInterval paces the idle session sweeper.
	SweepInterval time.Duration

	// MailboxSize bounds buffered commands of one device.
	MailboxSize int

	// MaxTelemetryBytes bounds an accepted telemetry request body.
	MaxTelemetryBytes int64

	// MaxMediaBytes bounds one uploaded media object.
	MaxMediaBytes int64
}

// session is one live HTTP presence session with its command mailbox.
type session struct {
	// identity is the authenticated device identity.
	identity sdktelemetry.DeviceIdentity

	// sessionID is the presence fencing token.
	sessionID string

	// lastSeen is the last authenticated request time.
	lastSeen time.Time

	// mailbox buffers dispatched commands waiting for the next poll.
	mailbox chan *v1.Command
}

// Server owns device sessions and the HTTP handler.
type Server struct {
	auth      Authenticator
	presence  PresenceManager
	ingressID string
	js        jetstream.JetStream
	// resolver supplies profile descriptors for the uplink path.
	resolver sdktelemetry.DescriptorResolver

	// relay correlates dispatched commands with response uplinks.
	relay *Relay

	// shadowReporter accepts device shadow reports.
	shadowReporter ShadowReporter

	// encoder compresses envelopes when bus compression is enabled.
	encoder sdktelemetry.FrameEncoder

	// deviceLimit applies the per-device token bucket.
	deviceLimit DeviceRateLimiter

	// accounting samples this ingress for ADR-0025 resource accounting.
	// It is optional: nil disables accounting for this plane.
	accounting accounting.Observer

	// ota serves device firmware OTA (ADR-0006b); nil disables OTA.
	ota OTADeviceService
	// otaBlobs streams authorized firmware objects for package download.
	otaBlobs BlobStreamer

	// mediaBlobs stores and serves device media objects (ADR-0006); nil
	// makes the blob endpoints return 503.
	mediaBlobs MediaBlobs
	// mediaStore offloads binary frame fields through the shared policy.
	mediaStore sdktelemetry.MediaStore

	tune Tuning

	mu       sync.Mutex
	sessions map[string]*session
}

// NewServer constructs the HTTP ingress server.
func NewServer(ingressID string, auth Authenticator, presence PresenceManager, js jetstream.JetStream, resolver sdktelemetry.DescriptorResolver, tune Tuning) *Server {
	return &Server{
		auth:      auth,
		presence:  presence,
		ingressID: ingressID,
		js:        js,
		resolver:  resolver,
		tune:      tune,
		sessions:  map[string]*session{},
	}
}

// SetRelay wires the command relay after construction.
func (s *Server) SetRelay(relay *Relay) {
	s.relay = relay
}

// SetShadowReporter wires shadow report handling after construction.
func (s *Server) SetShadowReporter(reporter ShadowReporter) {
	s.shadowReporter = reporter
}

// DeviceRateLimiter applies the per-device ingest token bucket. It is
// implemented by *ingestlimit.Limiter.
type DeviceRateLimiter interface {
	// Allow reports whether one frame from key (tenant.device) is admitted.
	Allow(key string, now time.Time) bool
}

// SetEncoder wires bus compression after construction.
func (s *Server) SetEncoder(encoder sdktelemetry.FrameEncoder) {
	s.encoder = encoder
}

// SetDeviceLimit wires per-device rate limiting after construction.
func (s *Server) SetDeviceLimit(limiter DeviceRateLimiter) {
	s.deviceLimit = limiter
}

// SetAccounting wires the resource accounting sampler into the HTTP
// telemetry uplink path.
func (s *Server) SetAccounting(observer accounting.Observer) {
	s.accounting = observer
}

// Handler returns the device-facing HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/me/telemetry", s.handleTelemetry)
	mux.HandleFunc("GET /v1/me/commands", s.handleCommands)
	mux.HandleFunc("POST /v1/me/cmdres/{command}", s.handleCommandResponse)
	mux.HandleFunc("POST /v1/me/shadow/report", s.handleShadowReport)
	mux.HandleFunc("GET /v1/me/ota/tasks", s.handleOTAList)
	mux.HandleFunc("POST /v1/me/ota/{task}/progress", s.handleOTAProgress)
	mux.HandleFunc("GET /v1/me/ota/{task}/package", s.handleOTAPackage)
	mux.HandleFunc("POST /v1/me/blob", s.handleBlobUpload)
	mux.HandleFunc("GET /v1/me/blob", s.handleBlobDownload)
	return mux
}

// Run starts the idle session sweeper and blocks until ctx is canceled.
// Sessions alive at shutdown are disconnected with their fencing tokens.
func (s *Server) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.tune.SweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return s.shutdown()
		case <-ticker.C:
			s.sweepIdle(ctx)
		}
	}
}

// SessionCount returns the number of currently open HTTP sessions.
func (s *Server) SessionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// sessionKey builds the session map key.
func sessionKey(tenantID, deviceID string) string {
	return tenantID + "." + deviceID
}

// SessionFor returns the live session of a device.
func (s *Server) SessionFor(tenantID, deviceID string) (*session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.sessions[sessionKey(tenantID, deviceID)]
	return current, ok
}

// authenticate resolves the device identity from the request. Basic
// carries the credential pair id:secret (secret-bearing credentials);
// Bearer presents a lone token matched against passwordless
// credentials. Both reject malformed input without a registry round
// trip.
func (s *Server) authenticate(r *http.Request) (sdktelemetry.DeviceIdentity, bool) {
	header := r.Header.Get("Authorization")

	if raw, ok := strings.CutPrefix(header, "Basic "); ok {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
		if err != nil {
			return sdktelemetry.DeviceIdentity{}, false
		}
		credentialsID, secret, _ := strings.Cut(string(decoded), ":")
		if credentialsID == "" {
			return sdktelemetry.DeviceIdentity{}, false
		}
		identity, ok, err := s.auth.Authenticate(r.Context(), credentialsID, secret)
		if err != nil || !ok {
			return sdktelemetry.DeviceIdentity{}, false
		}
		return identity, true
	}

	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		return sdktelemetry.DeviceIdentity{}, false
	}

	identity, ok, err := s.auth.Authenticate(r.Context(), token, "")
	if err != nil || !ok {
		return sdktelemetry.DeviceIdentity{}, false
	}
	return identity, true
}

// ensureSession returns the live session or opens one. The session is
// registered locally before presence.Connect so a command dispatch
// triggered by the resulting ONLINE event always finds its mailbox.
func (s *Server) ensureSession(ctx context.Context, identity sdktelemetry.DeviceIdentity) (*session, error) {
	key := sessionKey(identity.TenantID, identity.DeviceID)

	s.mu.Lock()
	if current, ok := s.sessions[key]; ok {
		current.lastSeen = time.Now()
		s.mu.Unlock()
		return current, nil
	}

	current := &session{
		identity: identity,
		lastSeen: time.Now(),
		mailbox:  make(chan *v1.Command, s.tune.MailboxSize),
	}
	s.sessions[key] = current
	s.mu.Unlock()

	sessionID, err := s.presence.Connect(ctx, presence.Identity{
		TenantID: identity.TenantID,
		DeviceID: identity.DeviceID,
	}, s.ingressID, "")
	if err != nil {
		s.removeSession(key)
		return nil, err
	}
	current.sessionID = sessionID
	return current, nil
}

// touch refreshes the idle timer of a session.
func (s *Server) touch(current *session) {
	s.mu.Lock()
	current.lastSeen = time.Now()
	s.mu.Unlock()
}

// removeSession drops a session after failed connect or idle expiry.
func (s *Server) removeSession(key string) {
	s.mu.Lock()
	delete(s.sessions, key)
	s.mu.Unlock()
}

// sweepIdle disconnects and removes sessions idle longer than SessionTTL.
// Long-poll requests refresh the timer on entry, so continuously polling
// devices never expire.
func (s *Server) sweepIdle(ctx context.Context) {
	now := time.Now()
	idle := []*session{}

	s.mu.Lock()
	for key, current := range s.sessions {
		if now.Sub(current.lastSeen) > s.tune.SessionTTL {
			idle = append(idle, current)
			delete(s.sessions, key)
		}
	}
	s.mu.Unlock()

	for _, current := range idle {
		if err := s.presence.Disconnect(ctx, presence.Identity{
			TenantID: current.identity.TenantID,
			DeviceID: current.identity.DeviceID,
		}, current.sessionID); err != nil {
			continue
		}
	}
}

// shutdown disconnects every locally owned session so presence converges
// to offline immediately instead of waiting for derived expiry.
func (s *Server) shutdown() error {
	ctx := context.Background()
	s.mu.Lock()
	sessions := make([]*session, 0, len(s.sessions))
	for key, current := range s.sessions {
		sessions = append(sessions, current)
		delete(s.sessions, key)
	}
	s.mu.Unlock()

	for _, current := range sessions {
		_ = s.presence.Disconnect(ctx, presence.Identity{
			TenantID: current.identity.TenantID,
			DeviceID: current.identity.DeviceID,
		}, current.sessionID)
	}
	return nil
}
