// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package ingestmqtt implements the southbound MQTT ingress service.
//
// The service embeds the mochi-mqtt broker (MIT licensed) and platformizes
// it through two hooks:
//
//   - AuthHook delegates connection authentication to the registry service
//     and remembers the resolved identity for the connection lifetime.
//   - PublishHook converts published device frames into canonical Envelope
//     messages and publishes them to the partitioned JetStream topology.
//
// No device identity knowledge or business logic lives in this package.
package ingestmqtt

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"sync"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/nereidai/nereidlink/internal/accounting"
	"github.com/nereidai/nereidlink/internal/connector"
	"github.com/nereidai/nereidlink/internal/contract"
	"github.com/nereidai/nereidlink/internal/deviceevent"
	"github.com/nereidai/nereidlink/internal/discovery"
	"github.com/nereidai/nereidlink/internal/edge"
	"github.com/nereidai/nereidlink/internal/edgeprofile"
	"github.com/nereidai/nereidlink/internal/linkhealth"
	"github.com/nereidai/nereidlink/internal/manifest"
	"github.com/nereidai/nereidlink/internal/modelinventory"
	"github.com/nereidai/nereidlink/internal/mqttapi"
	"github.com/nereidai/nereidlink/internal/presence"
	"github.com/nereidai/nereidlink/internal/profilepush"
	"github.com/nereidai/nereidlink/internal/secretbinding"
	"github.com/nereidai/nereidlink/internal/shadow"
	"github.com/nereidai/nereidlink/sdk/profile"
	"github.com/nereidai/nereidlink/sdk/scriptruntime"
	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"
)

// Authenticator resolves presented MQTT credentials to a device identity.
// It is implemented by the registry gRPC client, keeping ingress decoupled
// from the persistence layer.
type Authenticator interface {
	// Authenticate validates credentials and returns the resolved identity.
	Authenticate(ctx context.Context, username, password string) (sdktelemetry.DeviceIdentity, bool, error)
}

// CertificateAuthenticator resolves a leaf certificate presented at the
// mTLS layer to a device identity. Implementations must fail closed when
// the fingerprint matches no registered X509 credential.
type CertificateAuthenticator interface {
	// AuthenticateCertificate validates the SHA-256 leaf fingerprint.
	AuthenticateCertificate(ctx context.Context, credentialsID, fingerprint string) (sdktelemetry.DeviceIdentity, bool, error)
}

// PresenceManager starts and ends transport sessions in the presence layer.
// It is implemented by *presence.Service.
type PresenceManager interface {
	// Connect registers the session and returns its fencing session ID.
	Connect(ctx context.Context, id presence.Identity, ingressID, gatewayID string) (string, error)

	// Disconnect ends the session only when sessionID still fences it.
	Disconnect(ctx context.Context, id presence.Identity, sessionID string) error

	// DisconnectGateway ends every sub-device session attached to the
	// gateway and the gateway's own session in one cascade.
	DisconnectGateway(ctx context.Context, tenantID, gatewayID string) error

	// EnsureGatewayOffline ends one sub-device presence only while it is
	// attached to gatewayID, emitting an OFFLINE boundary independent of
	// the fencing session that attached it. Used by the explicit gateway
	// disconnect announcement (whose sub-device sessions carry no fencing
	// session id on this connection).
	EnsureGatewayOffline(ctx context.Context, id presence.Identity, gatewayID, reason string) (bool, error)
}

// GatewayManager authorizes sub-device attach requests on behalf of an
// authenticated gateway. It is implemented by the registry admin service.
type GatewayManager interface {
	// AssertGatewaySubdevice reports whether the gateway may attach the
	// sub-device in the tenant.
	AssertGatewaySubdevice(ctx context.Context, tenantID, gatewayID, deviceID string) (bool, error)
	// StampGatewayFeatures records the negotiated feature set after a
	// successful hello (ADR-0034).
	StampGatewayFeatures(ctx context.Context, tenantID, gatewayID string, features []string) error
	// StampGatewayOperations records the operation keys the gateway
	// advertised after a successful hello (ADR-0035).
	StampGatewayOperations(ctx context.Context, tenantID, gatewayID string, operations []string) error
	// StampGatewayProfileFormats records the profile document formats the
	// gateway declared (and the platform enabled) after a successful hello
	// (ADR-0038).
	StampGatewayProfileFormats(ctx context.Context, tenantID, gatewayID string, formats []string) error
	// StampGatewayInference records the accepted edge model-inference
	// capability (contract + backend intersection) after a successful hello
	// (ADR-0039). An empty contract stamps an empty capability.
	StampGatewayInference(ctx context.Context, tenantID, gatewayID, contract string, backends []string) error
}

// ManifestReconciler reconciles a gateway's periodic full sub-device
// manifest (ADR-0026). It is implemented by *manifest.Reconciler.
type ManifestReconciler interface {
	Reconcile(ctx context.Context, tenantID, gatewayID string, frame manifest.Frame) (manifest.Report, error)
}

// ConnectorStatusReceiver consumes edge-reported connector lifecycle status
// (ADR-0026). It is implemented by *connector.Service.
type ConnectorStatusReceiver interface {
	HandleStatus(ctx context.Context, tenantID string, f connector.StatusFrame) (connector.Spec, error)
}

// ConnectorRedriver re-sends the full desired connector set for a gateway
// once it completes hello. It is implemented by *connector.Service. Firing
// here (rather than on the raw transport ONLINE event) guarantees the edge
// has already subscribed to the connector downlink filter, so the re-sent
// specs cannot be lost to a connect/subscribe race.
type ConnectorRedriver interface {
	Redrive(ctx context.Context, tenantID, gatewayID string)
}

// SecretStatusReceiver consumes edge-reported secret delivery status
// (ADR-0034). It is implemented by *secretbinding.Service.
type SecretStatusReceiver interface {
	HandleStatus(ctx context.Context, tenantID, gatewayID string, frame secretbinding.StatusFrame) error
}

// SecretRedriver re-sends the full desired secret set for a gateway once
// it completes hello and advertises the "secrets" feature. It is
// implemented by *secretbinding.Service.
type SecretRedriver interface {
	Redrive(ctx context.Context, tenantID, gatewayID string) error
}

// ProfileStatusReceiver consumes edge-reported profile delivery status
// (ADR-0038). It is implemented by *profilepush.Service.
type ProfileStatusReceiver interface {
	HandleStatus(ctx context.Context, tenantID, gatewayID string, frame profilepush.StatusFrame) error
}

// ProfileRedriver re-sends the full desired profile set for a gateway once
// it completes hello and advertises the "profiles" feature. It is
// implemented by *profilepush.Service.
type ProfileRedriver interface {
	Redrive(ctx context.Context, tenantID, gatewayID string) error
}

// ConnectorManifestReconciler consumes the optional connector segment of a
// gateway manifest (ADR-0026 strict reconciliation, migration 0044) and
// returns the desired/reported partition. It is implemented by
// *connector.Service.
type ConnectorManifestReconciler interface {
	ReconcileReported(ctx context.Context, tenantID, gatewayID string, reported []connector.ReportedInstance) (connector.ReconcileReport, error)
}

// ModelInventoryReconciler consumes the optional full-replacement models
// segment of a gateway manifest (ADR-0039). It is implemented by
// *modelinventory.Service. The ingress maps the transport segment to the
// package's transport-independent Input slice.
type ModelInventoryReconciler interface {
	Reconcile(ctx context.Context, tenantID, gatewayID string, reported []modelinventory.Input, at time.Time) (modelinventory.ReconcileResult, error)
}

// DiscoveryReceiver handles the gateway auto-provisioning uplink
// (ADR-0026/ADR-0013). It materialises reported local devices under policy and
// returns the local_id -> device_id mapping the handler sends back to the
// edge. It is implemented by *discovery.Service.
type DiscoveryReceiver interface {
	HandleDiscovery(ctx context.Context, tenantID, gatewayID string, f discovery.Frame) (discovery.Result, error)
}

// EdgeProfileReceiver consumes EdgeX device profiles reported on
// v1/gateway/profiles and caches them for later adoption. It is implemented
// by *edgeprofile.Service. The gateway identity is supplied by the hook from
// the authenticated, negotiated session; the frame never carries trusted
// tenant or gateway fields.
type EdgeProfileReceiver interface {
	Ingest(ctx context.Context, tenantID, gatewayID string, frame edgeprofile.Frame) error
}

// ProbeReporter forwards a gateway per-device probe result uplink onto the
// Core NATS event channel consumed by the probe manager (ADR-0026 section 9).
// It is implemented by the ingest probe relay.
type ProbeReporter interface {
	ReportResult(tenantID, gatewayID string, payload []byte)
}

// OTAReporter forwards a gateway per-device firmware progress uplink onto the
// Core NATS event channel consumed by the OTA manager (ADR-0026). It is
// implemented by the ingest OTA relay.
type OTAReporter interface {
	ReportProgress(tenantID, gatewayID string, payload []byte)
}

// LinkHealthObserver consumes application-link liveness signals for the
// ADR-0026 watchdog (SUSPECT/DOWN/flap). It is implemented by
// *linkhealth.Monitor. It is optional; a nil observer disables watchdog
// tracking without affecting transport behavior.
type LinkHealthObserver interface {
	ObserveConnect(ctx context.Context, tenantID, gatewayID string, at time.Time)
	ObserveHeartbeat(ctx context.Context, tenantID, gatewayID string, at time.Time)
	ObserveGoodbye(ctx context.Context, tenantID, gatewayID string, planned bool, reason string, at time.Time)
	ObserveTransportClosed(ctx context.Context, tenantID, gatewayID string, at time.Time)
	ObserveEdgeEpisode(ctx context.Context, tenantID, gatewayID string, at time.Time)
}

// MeasurementResolver resolves effective descriptors for a device.
type MeasurementResolver interface {
	// Resolve returns the active profile descriptors when the device has one.
	Resolve(ctx context.Context, tenantID, deviceID string) (profile.Profile, map[string]profile.Descriptor, error)

	// ExpressionRuntime returns the runtime for expression descriptors.
	ExpressionRuntime() *scriptruntime.Runtime
}

// connection caches the per-connection identity and fencing session.
type connection struct {
	// identity is the resolved device identity.
	identity sdktelemetry.DeviceIdentity

	// sessionID is the presence fencing token.
	sessionID string

	// subdevices records the tenant.device keys announced by a gateway.
	// It lets gateway disconnection cascade to every attached sub-device.
	subdevices map[string]struct{}

	// negotiated is true once a gateway completed hello. Before that the
	// connection is UNIDENTIFIED and gateway data topics are rejected.
	negotiated bool

	// edgeSessionID is the session id returned by hello negotiation.
	edgeSessionID string

	// features is the negotiated feature intersection (ack.features), used
	// to gate feature-specific downlinks such as secret delivery so old
	// firmware never receives a frame it cannot parse (ADR-0034).
	features []string
}

// AuthHook performs MQTT connection authentication, presence lifecycle and
// topic-template ACL for each connection.
type AuthHook struct {
	mqtt.HookBase

	// auth is the delegated credential verifier.
	auth Authenticator

	// presence starts and ends sessions.
	presence PresenceManager

	// ingressID labels sessions opened by this MQTT ingress.
	ingressID string

	// mu guards connections.
	mu sync.Mutex

	// connections maps mochi connection IDs to connection state.
	connections map[string]connection

	// devices maps "tenant.device" to the mochi connection ID for targeted
	// command delivery.
	devices map[string]string

	// gatewayOf maps "tenant.sub-device" to the live gateway client ID that
	// attached it. It is the reverse index of connection.subdevices and lets
	// downlink frames (command/attribute push) find the owning gateway in
	// O(1) instead of scanning every connection. Maintained together with
	// record/drop/replace/remove under mu.
	gatewayOf map[string]string

	// onGatewayConnect, when set, is notified after a gateway-capability
	// connection authenticates. The hello enforcer uses it to arm the
	// negotiation timeout; it stays nil in tests that do not care.
	onGatewayConnect func(clientID string)

	// revocation, when set, rejects mTLS certificates reported revoked by
	// their issuer OCSP responder (or CRL) before registry authentication
	// (ADR-0009).
	revocation RevocationChecker

	// linkHealth, when set, records transport disconnects for the ADR-0026
	// gateway watchdog. Nil disables watchdog tracking.
	linkHealth LinkHealthObserver
}

// RevocationChecker reports whether a presented client certificate is
// revoked. An error means the check could not be completed; the caller
// applies the platform failure policy (deny on error, fail closed).
type RevocationChecker interface {
	Revoked(ctx context.Context, cert, issuer *x509.Certificate, overrideURL string) (bool, error)
}

// SetRevocationChecker wires the OCSP/CRL revocation checker for mTLS certs.
func (h *AuthHook) SetRevocationChecker(checker RevocationChecker) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.revocation = checker
}

// NewAuthHook constructs an authentication hook backed by auth and presence.
func NewAuthHook(auth Authenticator, presence PresenceManager, ingressID string) *AuthHook {
	return &AuthHook{
		auth:        auth,
		presence:    presence,
		ingressID:   ingressID,
		connections: map[string]connection{},
		devices:     map[string]string{},
		gatewayOf:   map[string]string{},
	}
}

// ID returns the unique hook identifier registered with the broker.
func (h *AuthHook) ID() string { return "nl-auth" }

// Provides declares the hook events consumed from mochi.
func (h *AuthHook) Provides(b byte) bool {
	return b == mqtt.OnConnectAuthenticate || b == mqtt.OnACLCheck ||
		b == mqtt.OnDisconnect
}

// OnACLCheck authorizes publish or subscribe actions using the frozen topic
// templates. Deny is the default so an unrecognized topic never reaches the
// platform.
//
// The decision is delegated to mqttapi.Authorize, the same function the
// registry gRPC Authorize RPC uses. Keeping a single definition is what
// guarantees an external ingress connector that asks the registry receives
// the same answer this in-process broker gives; two ACL copies are two ACLs
// that will eventually disagree.
func (h *AuthHook) OnACLCheck(cl *mqtt.Client, topic string, write bool) bool {
	current, ok := h.connectionFor(cl.ID)
	if !ok {
		return false
	}
	action := mqttapi.ActionSubscribe
	if write {
		action = mqttapi.ActionPublish
	}
	return mqttapi.Authorize(action, topic, current.identity.Capabilities)
}

// OnConnect starts the presence session after the broker accepts the
// connection.
func (h *AuthHook) OnConnect(cl *mqtt.Client, pk packets.Packet) error {
	current, ok := h.connectionFor(cl.ID)
	if !ok {
		return nil
	}

	sessionID, err := h.presence.Connect(context.Background(), presence.Identity{
		TenantID: current.identity.TenantID,
		DeviceID: current.identity.DeviceID,
	}, h.ingressID, "")
	if err != nil {
		return fmt.Errorf("start presence session: %w", err)
	}

	current.sessionID = sessionID
	h.mu.Lock()
	h.connections[cl.ID] = current
	h.devices[deviceKey(current.identity.TenantID, current.identity.DeviceID)] = cl.ID
	h.mu.Unlock()
	return nil
}

// OnDisconnect ends the presence session and drops the cached connection so
// memory stays flat across client churn. Gateway sessions cascade OFFLINE to
// every attached sub-device before their own session ends.
func (h *AuthHook) OnDisconnect(cl *mqtt.Client, err error, expire bool) {
	current, ok := h.connectionFor(cl.ID)
	if !ok {
		return
	}
	if isGatewayIdentity(current.identity) {
		if h.linkHealth != nil {
			// Notify the watchdog of the physical disconnect. When the
			// edge first sent a planned goodbye (or the broker delivered
			// its LWT after a planned close) the detector dedupes the
			// episode; an unexpected close is an ungraceful transport
			// down and counts toward flapping.
			h.linkHealth.ObserveTransportClosed(context.Background(),
				current.identity.TenantID, current.identity.DeviceID, time.Now().UTC())
		}
		if gatewayErr := h.presence.DisconnectGateway(context.Background(),
			current.identity.TenantID, current.identity.DeviceID); gatewayErr != nil {
			h.Log.Warn("gateway presence cascade failed", "error", gatewayErr)
		}
	} else if disconnectErr := h.presence.Disconnect(context.Background(), presence.Identity{
		TenantID: current.identity.TenantID,
		DeviceID: current.identity.DeviceID,
	}, current.sessionID); disconnectErr != nil {
		h.Log.Warn("presence disconnect failed", "error", disconnectErr)
	}
	h.removeConnection(cl.ID)
}

// removeConnection drops the cached state after disconnection.
func (h *AuthHook) removeConnection(clientID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	current, ok := h.connections[clientID]
	if ok {
		delete(h.devices, deviceKey(current.identity.TenantID, current.identity.DeviceID))
		// A gateway connection owns the reverse index for every sub-device
		// it attached; drop those entries so a later downlink does not
		// resolve to a dead client.
		for key := range current.subdevices {
			if owner, present := h.gatewayOf[key]; present && owner == clientID {
				delete(h.gatewayOf, key)
			}
		}
	}
	delete(h.connections, clientID)
}

// SetGatewayConnectCallback registers the function notified when a
// gateway connection authenticates. Used to arm the hello timeout.
func (h *AuthHook) SetGatewayConnectCallback(fn func(clientID string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onGatewayConnect = fn
}

// SetLinkHealthObserver wires the ADR-0026 link watchdog into transport
// disconnect handling. Nil disables observation.
func (h *AuthHook) SetLinkHealthObserver(observer LinkHealthObserver) {
	h.linkHealth = observer
}

// clientIDFor resolves the live broker client ID of a device for targeted
// command delivery.
func (h *AuthHook) clientIDFor(tenantID, deviceID string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id, ok := h.devices[deviceKey(tenantID, deviceID)]
	return id, ok
}

// recordSubdevice adds a gateway-attached sub-device to the connection so a
// later gateway disconnect cascades OFFLINE to it.
func (h *AuthHook) recordSubdevice(clientID, tenantID, deviceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	current, ok := h.connections[clientID]
	if !ok {
		return
	}
	if current.subdevices == nil {
		current.subdevices = map[string]struct{}{}
	}
	if h.gatewayOf == nil {
		h.gatewayOf = map[string]string{}
	}
	key := deviceKey(tenantID, deviceID)
	current.subdevices[key] = struct{}{}
	h.gatewayOf[key] = clientID
	h.connections[clientID] = current
}

// dropSubdevice removes one sub-device from a gateway connection.
func (h *AuthHook) dropSubdevice(clientID, tenantID, deviceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	current, ok := h.connections[clientID]
	if !ok {
		return
	}
	key := deviceKey(tenantID, deviceID)
	delete(current.subdevices, key)
	if owner, present := h.gatewayOf[key]; present && owner == clientID {
		delete(h.gatewayOf, key)
	}
	h.connections[clientID] = current
}

// isSubdevice reports whether a device was attached to the connection.
func (h *AuthHook) isSubdevice(clientID, tenantID, deviceID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	current, ok := h.connections[clientID]
	if !ok {
		return false
	}
	_, attached := current.subdevices[deviceKey(tenantID, deviceID)]
	return attached
}

// replaceSubdevices resets the connection's attached set to exactly the
// supplied devices. It is used after a full-list manifest so the local
// authorization set converges to the authoritative in-sync list, including
// removing entries a previous frame attached but the current one omits.
func (h *AuthHook) replaceSubdevices(clientID, tenantID string, deviceIDs []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	current, ok := h.connections[clientID]
	if !ok {
		return
	}
	next := make(map[string]struct{}, len(deviceIDs))
	for _, deviceID := range deviceIDs {
		next[deviceKey(tenantID, deviceID)] = struct{}{}
	}
	if h.gatewayOf == nil {
		h.gatewayOf = map[string]string{}
	}
	// Converge the reverse index: detach entries this gateway no longer owns,
	// then point the in-sync set at this connection.
	for key, owner := range h.gatewayOf {
		if owner == clientID {
			if _, stillAttached := next[key]; !stillAttached {
				delete(h.gatewayOf, key)
			}
		}
	}
	for key := range next {
		h.gatewayOf[key] = clientID
	}
	current.subdevices = next
	h.connections[clientID] = current
}

// gatewayClientIDFor resolves the live client ID that attached the sub-device.
// It returns false when no gateway on this ingress has attached the device.
func (h *AuthHook) gatewayClientIDFor(tenantID, deviceID string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id, ok := h.gatewayOf[deviceKey(tenantID, deviceID)]
	return id, ok
}

// isGatewayIdentity reports whether the resolved identity carries the
// gateway capability granted by the registry.
func isGatewayIdentity(identity sdktelemetry.DeviceIdentity) bool {
	for _, capability := range identity.Capabilities {
		if capability == mqttapi.CapabilityGateway {
			return true
		}
	}
	return false
}

// deviceKey builds the reverse-map key.
func deviceKey(tenantID, deviceID string) string {
	return tenantID + "." + deviceID
}

// peerCertFingerprint returns the lowercase hex SHA-256 fingerprint of the
// leaf certificate presented on a TLS listener connection. It returns an
// empty string for plain TCP connections or when no peer certificate is
// present. The chain has already been verified against the listener CA.
func peerCertFingerprint(cl *mqtt.Client) string {
	cert := peerCertificate(cl)
	if cert == nil {
		return ""
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// peerCertificate returns the leaf certificate presented on a TLS listener
// connection, or nil for plain TCP or a connection without a peer cert.
func peerCertificate(cl *mqtt.Client) *x509.Certificate {
	tlsConn, ok := cl.Net.Conn.(*tls.Conn)
	if !ok {
		return nil
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return nil
	}
	return state.PeerCertificates[0]
}

// peerIssuer returns the certificate that signed the presented leaf
// certificate on a TLS listener connection, or nil for plain TCP, a
// connection without a peer cert, or a self-signed leaf.
func peerIssuer(cl *mqtt.Client) *x509.Certificate {
	tlsConn, ok := cl.Net.Conn.(*tls.Conn)
	if !ok {
		return nil
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) < 2 {
		return nil
	}
	return state.PeerCertificates[1]
}

// OnConnectAuthenticate verifies CONNECT credentials through registry,
// starts the presence session and caches the connection state. Presence is
// started here rather than in OnConnect because mochi dispatches OnConnect
// before the authentication event.
func (h *AuthHook) OnConnectAuthenticate(cl *mqtt.Client, pk packets.Packet) bool {
	ctx := context.Background()
	fingerprint := peerCertFingerprint(cl)

	// OCSP (with a CRL fallback) revocation is checked before registry
	// authentication; an error denies the connection (fail closed), per the
	// ADR-0009 policy that revocation results are short-TTL cached but never
	// silently skipped.
	if fingerprint != "" && h.revocation != nil {
		if revoked, err := h.revocation.Revoked(ctx, peerCertificate(cl), peerIssuer(cl), ""); err != nil {
			h.Log.Error("certificate revocation check failed", "error", err)
			return false
		} else if revoked {
			return false
		}
	}

	// On the mTLS listener the leaf certificate is the proof of
	// possession; username still names the X509 credential. On the plain
	// TCP listener the password proves possession as before.
	var identity sdktelemetry.DeviceIdentity
	var ok bool
	var err error
	if fingerprint != "" {
		certAuth, supported := h.auth.(CertificateAuthenticator)
		if !supported {
			h.Log.Error("registry backend does not support certificate authentication")
			return false
		}
		identity, ok, err = certAuth.AuthenticateCertificate(ctx,
			string(pk.Connect.Username), fingerprint)
	} else {
		identity, ok, err = h.auth.Authenticate(ctx,
			string(pk.Connect.Username), string(pk.Connect.Password))
	}
	if err != nil {
		h.Log.Error("registry authentication failed", "error", err)
		return false
	}
	if !ok {
		return false
	}

	sessionID, err := h.presence.Connect(context.Background(), presence.Identity{
		TenantID: identity.TenantID,
		DeviceID: identity.DeviceID,
	}, h.ingressID, "")
	if err != nil {
		h.Log.Error("presence session start failed", "error", err)
		return false
	}

	h.mu.Lock()
	h.connections[cl.ID] = connection{
		identity:  identity,
		sessionID: sessionID,
	}
	h.devices[deviceKey(identity.TenantID, identity.DeviceID)] = cl.ID
	h.mu.Unlock()

	if isGatewayIdentity(identity) && h.onGatewayConnect != nil {
		h.onGatewayConnect(cl.ID)
	}
	return true
}

// connectionFor returns the cached connection state.
func (h *AuthHook) connectionFor(clientID string) (connection, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	current, ok := h.connections[clientID]
	return current, ok
}

// isNegotiated reports whether the connection completed hello.
func (h *AuthHook) isNegotiated(clientID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	current, ok := h.connections[clientID]
	return ok && current.negotiated
}

// markNegotiated records the successful hello and edge session id.
func (h *AuthHook) markNegotiated(clientID, edgeSessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	current, ok := h.connections[clientID]
	if !ok {
		return
	}
	current.negotiated = true
	current.edgeSessionID = edgeSessionID
	h.connections[clientID] = current
}

// markFeatures records the feature intersection negotiated in hello so the
// downlink relay can gate feature-specific frames for the session.
func (h *AuthHook) markFeatures(clientID string, features []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	current, ok := h.connections[clientID]
	if !ok {
		return
	}
	current.features = features
	h.connections[clientID] = current
}

// clientHasFeature reports whether the live session negotiated the feature.
// An unknown client or a pre-hello session returns false (fail closed).
func (h *AuthHook) clientHasFeature(clientID, feature string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	current, ok := h.connections[clientID]
	if !ok {
		return false
	}
	return slices.Contains(current.features, feature)
}

// ShadowReporter accepts device shadow reports.
type ShadowReporter interface {
	// UpdateReported merges one device-reported patch.
	UpdateReported(ctx context.Context, deviceID string, patch shadow.ReportedPatch) (shadow.State, error)
}

// AttributeSaver upserts device-reported (CLIENT scope) attributes. It is
// implemented by *rule.AttrStore; the MQTT plane depends only on this narrow
// interface so uplink attribute frames never pull the rule engine into the
// ingress package. A device owns only CLIENT scope, which is why scope is not
// a parameter here.
type AttributeSaver interface {
	SaveAttributes(ctx context.Context, tenantID, deviceID, scope string, attrs map[string]any) error
}

// AttributeFetcher answers a device-initiated pull of platform-managed
// attributes (SERVER_SCOPE/SHARED_SCOPE). It is implemented by
// *registry.Admin. An empty scopes list means both platform scopes.
type AttributeFetcher interface {
	FetchAttributes(ctx context.Context, tenantID, deviceID string, scopes []string) (map[string]map[string]json.RawMessage, error)
}

// EventIngester persists device-published domain events (v1/me/events) and
// fans them out. It is implemented by *deviceevent.Service. Nil leaves the
// events topic unhandled (frames are dropped with a warning).
type EventIngester interface {
	Ingest(ctx context.Context, tenantID, deviceID string, frames []deviceevent.Frame) error
}

// PublishHook converts accepted MQTT PUBLISH packets into Envelope messages
// on JetStream.
type PublishHook struct {
	mqtt.HookBase

	// js is the target JetStream instance.
	js jetstream.JetStream

	// connections resolves connection IDs to connection state.
	connections interface {
		connectionFor(string) (connection, bool)
		recordSubdevice(clientID, tenantID, deviceID string)
		dropSubdevice(clientID, tenantID, deviceID string)
		replaceSubdevices(clientID, tenantID string, deviceIDs []string)
		isSubdevice(clientID, tenantID, deviceID string) bool
		isNegotiated(clientID string) bool
		markNegotiated(clientID, edgeSessionID string)
		markFeatures(clientID string, features []string)
	}

	// responses correlates command response uplinks.
	responses ResponseReporter

	// resolver supplies effective measurement descriptors.
	resolver MeasurementResolver

	// shadows accepts reported shadow patches.
	shadows ShadowReporter

	// shadowAuditor records rejected gateway shadow report entries.
	shadowAuditor shadowReportAuditor

	// attributes accepts device-reported CLIENT-scope attribute upserts.
	// Nil disables uplink attribute handling (frames are acknowledged but
	// not persisted).
	attributes AttributeSaver

	// attributeFetcher answers on-demand device/gateway pulls of
	// platform-managed attributes. Nil disables v1/.../attributes/get.
	attributeFetcher AttributeFetcher

	// events accepts device domain-event uplinks. Nil leaves v1/me/events
	// unhandled (frames are dropped with a warning rather than black-holed).
	events EventIngester

	// presence starts and ends sub-device sessions for gateway attach.
	presence PresenceManager

	// gateways authorizes sub-device attach/detach for gateway clients.
	gateways GatewayManager

	// edge runs transport-independent hello negotiation.
	edge EdgeNegotiator

	// manifests reconciles the periodic gateway sub-device manifest.
	manifests ManifestReconciler

	// connectors consumes edge-reported connector instance status.
	connectors ConnectorStatusReceiver

	// connectorRedrive re-sends the desired connector set after hello.
	connectorRedrive ConnectorRedriver

	// secretBindings consumes edge-reported secret delivery status
	// (ADR-0034). Nil disables status handling.
	secretBindings SecretStatusReceiver

	// secretRedrive re-sends the desired secret set after a hello whose
	// edge advertises the "secrets" feature.
	secretRedrive SecretRedriver

	// profileBindings consumes edge-reported profile delivery status
	// (ADR-0038). Nil disables status handling.
	profileBindings ProfileStatusReceiver

	// profileRedrive re-sends the desired profile set after a hello whose
	// edge advertises the "profiles" feature.
	profileRedrive ProfileRedriver

	// connectorManifests reconciles the connector segment of a gateway
	// manifest (migration 0044). Nil disables connector reconciliation.
	connectorManifests ConnectorManifestReconciler

	// modelInventory reconciles the optional full-replacement models segment
	// (ADR-0039, migration 0086). Nil disables model reconciliation.
	modelInventory ModelInventoryReconciler

	// connectorCatalog compiles and stores edge-advertised connector config
	// schemas from hello. Nil disables schema registration.
	connectorCatalog EdgeConnectorSchemaRegistrar

	// secretCatalog compiles and stores edge secret type descriptors from
	// hello (ADR-0034). Nil disables descriptor registration.
	secretCatalog EdgeSecretTypeRegistrar

	// operationCatalog stores edge-advertised gateway operation
	// descriptors from hello (ADR-0035). Nil disables registration.
	operationCatalog EdgeOperationRegistrar

	// discovery handles gateway auto-provisioning uplinks and their result
	// downlink. Nil disables auto-provisioning (frames are ignored).
	discovery DiscoveryReceiver

	// edgeProfiles caches edge-reported EdgeX profiles for later adoption.
	// Nil disables profile handling (frames are ignored).
	edgeProfiles EdgeProfileReceiver

	// probeResults forwards gateway per-device probe results. Nil disables
	// probe response handling.
	probeResults ProbeReporter

	// otaResults forwards gateway per-device firmware progress. Nil disables
	// OTA progress handling.
	otaResults OTAReporter

	// opResults forwards edge-reported whole-gateway operation results.
	// Nil disables operation result uplinks.
	opResults OperationResultReporter

	// linkHealth, when set, consumes application liveness signals for the
	// ADR-0026 watchdog. Nil disables watchdog tracking.
	linkHealth LinkHealthObserver

	// linkEpisodes applies edge-reported link episodes. Nil drops episode
	// reports; production always wires it.
	linkEpisodes LinkEpisodeStore

	// broker publishes negotiation acks to live clients.
	broker *mqtt.Server

	// helloEnforcer arms the per-connection negotiation timeout.
	helloEnforcer *helloEnforcer

	// encoder compresses envelopes when bus compression is enabled.
	encoder sdktelemetry.FrameEncoder

	// mediaStore offloads frame fields marked {blob:true}; nil rejects
	// them. It is optional because blob storage can be disabled.
	mediaStore sdktelemetry.MediaStore

	// deviceLimit applies the per-device token bucket before publishing.
	deviceLimit DeviceRateLimiter

	// accounting samples this ingress for ADR-0025 resource accounting.
	// It is optional: nil disables accounting for this plane.
	accounting accounting.Observer
}

// SetAccounting wires the resource accounting sampler into the MQTT
// publish path.
func (h *PublishHook) SetAccounting(observer accounting.Observer) {
	h.accounting = observer
}

// DeviceRateLimiter applies the per-device ingest token bucket. It is
// implemented by *ingestlimit.Limiter.
type DeviceRateLimiter interface {
	// Allow reports whether one frame from key (tenant.device) is admitted.
	Allow(key string, now time.Time) bool
}

// ResponseReporter handles device command response uplinks.
type ResponseReporter interface {
	// ReportResponse correlates a response with its pending command.
	ReportResponse(tenantID, deviceID, commandID string, payload []byte)
}

// NewPublishHook constructs the publish conversion hook.
func NewPublishHook(js jetstream.JetStream, connections interface {
	connectionFor(string) (connection, bool)
	recordSubdevice(clientID, tenantID, deviceID string)
	dropSubdevice(clientID, tenantID, deviceID string)
	replaceSubdevices(clientID, tenantID string, deviceIDs []string)
	isSubdevice(clientID, tenantID, deviceID string) bool
	isNegotiated(clientID string) bool
	markNegotiated(clientID, edgeSessionID string)
	markFeatures(clientID string, features []string)
}, presence PresenceManager, responses ResponseReporter, resolver MeasurementResolver) *PublishHook {
	return &PublishHook{
		js:          js,
		connections: connections,
		presence:    presence,
		responses:   responses,
		resolver:    resolver,
	}
}

// SetShadowReporter wires shadow report handling.
func (h *PublishHook) SetShadowReporter(reporter ShadowReporter) {
	h.shadows = reporter
}

// SetShadowAuditor wires audit recording of rejected gateway shadow entries.
func (h *PublishHook) SetShadowAuditor(auditor shadowReportAuditor) {
	h.shadowAuditor = auditor
}

// SetAttributeSaver wires device-reported (CLIENT scope) attribute handling.
func (h *PublishHook) SetAttributeSaver(saver AttributeSaver) {
	h.attributes = saver
}

// SetAttributeFetcher wires on-demand device pulls of platform-managed
// attributes (v1/me/attributes/get and the gateway-proxied variant).
func (h *PublishHook) SetAttributeFetcher(fetcher AttributeFetcher) {
	h.attributeFetcher = fetcher
}

// SetEventIngester wires device domain-event uplinks (v1/me/events).
func (h *PublishHook) SetEventIngester(ingester EventIngester) {
	h.events = ingester
}

// SetGatewayManager wires gateway sub-device authorization.
func (h *PublishHook) SetGatewayManager(manager GatewayManager) {
	h.gateways = manager
}

// SetEdgeNegotiator wires hello negotiation.
func (h *PublishHook) SetEdgeNegotiator(negotiator EdgeNegotiator) {
	h.edge = negotiator
}

// SetManifestReconciler wires manifest reconciliation into the publish hook.
func (h *PublishHook) SetManifestReconciler(reconciler ManifestReconciler) {
	h.manifests = reconciler
}

// SetConnectorStatusReceiver wires connector status uplinks into the
// connector lifecycle service (ADR-0026). Nil disables status handling.
func (h *PublishHook) SetConnectorStatusReceiver(receiver ConnectorStatusReceiver) {
	h.connectors = receiver
}

// SetConnectorRedriver wires the post-hello desired-spec re-send.
func (h *PublishHook) SetConnectorRedriver(redriver ConnectorRedriver) {
	h.connectorRedrive = redriver
}

// SetSecretStatusReceiver wires secret delivery status uplinks into the
// binding service (ADR-0034). Nil disables status handling.
func (h *PublishHook) SetSecretStatusReceiver(receiver SecretStatusReceiver) {
	h.secretBindings = receiver
}

// SetSecretRedriver wires the post-hello desired-secret re-send.
func (h *PublishHook) SetSecretRedriver(redriver SecretRedriver) {
	h.secretRedrive = redriver
}

// SetProfileStatusReceiver wires profile delivery status uplinks into the
// delivery service (ADR-0038). Nil disables status handling.
func (h *PublishHook) SetProfileStatusReceiver(receiver ProfileStatusReceiver) {
	h.profileBindings = receiver
}

// SetProfileRedriver wires the post-hello desired-profile re-send.
func (h *PublishHook) SetProfileRedriver(redriver ProfileRedriver) {
	h.profileRedrive = redriver
}

// SetConnectorManifestReconciler wires the connector segment of gateway
// manifest reconciliation (ADR-0026 strict reconcile, migration 0044).
func (h *PublishHook) SetConnectorManifestReconciler(reconciler ConnectorManifestReconciler) {
	h.connectorManifests = reconciler
}

// SetModelInventoryReconciler wires the models segment of gateway manifest
// reconciliation (ADR-0039, migration 0086).
func (h *PublishHook) SetModelInventoryReconciler(reconciler ModelInventoryReconciler) {
	h.modelInventory = reconciler
}

// SetConnectorSchemaRegistrar wires edge-advertised config-schema
// registration from hello (ADR-0026 schema validation slice).
func (h *PublishHook) SetConnectorSchemaRegistrar(registrar EdgeConnectorSchemaRegistrar) {
	h.connectorCatalog = registrar
}

// SetSecretTypeRegistrar wires edge-advertised secret type descriptor
// registration from hello (ADR-0034).
func (h *PublishHook) SetSecretTypeRegistrar(registrar EdgeSecretTypeRegistrar) {
	h.secretCatalog = registrar
}

// SetOperationRegistrar wires edge-advertised gateway operation
// registration from hello (ADR-0035).
func (h *PublishHook) SetOperationRegistrar(registrar EdgeOperationRegistrar) {
	h.operationCatalog = registrar
}

// SetDiscoveryReceiver wires gateway auto-provisioning uplinks into the
// discovery service (ADR-0026/ADR-0013). Nil disables auto-provisioning.
func (h *PublishHook) SetDiscoveryReceiver(receiver DiscoveryReceiver) {
	h.discovery = receiver
}

// SetEdgeProfileReceiver wires edge-reported EdgeX profile uplinks into the
// edge profile cache/adopt service. Nil disables profile handling.
func (h *PublishHook) SetEdgeProfileReceiver(receiver EdgeProfileReceiver) {
	h.edgeProfiles = receiver
}

// SetProbeReporter wires gateway probe result uplinks into the probe relay.
func (h *PublishHook) SetProbeReporter(reporter ProbeReporter) {
	h.probeResults = reporter
}

// SetOTAReporter wires gateway firmware progress uplinks into the OTA relay.
func (h *PublishHook) SetOTAReporter(reporter OTAReporter) {
	h.otaResults = reporter
}

// OperationResultReporter accepts edge-reported operation result uplinks.
// *OperationRelay satisfies it.
type OperationResultReporter interface {
	// ReportResult correlates one result payload with its invocation.
	ReportResult(payload []byte)
}

// SetOperationResultReporter wires gateway operation result uplinks into
// the operation relay.
func (h *PublishHook) SetOperationResultReporter(reporter OperationResultReporter) {
	h.opResults = reporter
}

// SetLinkHealthObserver wires the ADR-0026 link watchdog into uplink
// handling. Nil disables watchdog observations.
func (h *PublishHook) SetLinkHealthObserver(observer LinkHealthObserver) {
	h.linkHealth = observer
}

// SetBroker enables publishing negotiation acks and arms the enforcer.
func (h *PublishHook) SetBroker(broker *mqtt.Server, enforcer *helloEnforcer) {
	h.broker = broker
	h.helloEnforcer = enforcer
}

// SetEncoder wires bus compression into telemetry publishes.
func (h *PublishHook) SetEncoder(encoder sdktelemetry.FrameEncoder) {
	h.encoder = encoder
}

// SetMediaStore wires blob offload for marked frame fields.
func (h *PublishHook) SetMediaStore(store sdktelemetry.MediaStore) {
	h.mediaStore = store
}

// SetDeviceLimit wires per-device rate limiting.
func (h *PublishHook) SetDeviceLimit(limiter DeviceRateLimiter) {
	h.deviceLimit = limiter
}

// ID returns the unique hook identifier registered with the broker.
func (h *PublishHook) ID() string { return "nl-publish" }

// Provides declares that the hook observes published packets.
func (h *PublishHook) Provides(b byte) bool { return b == mqtt.OnPublished }

// OnPublished handles command response uplinks and converts telemetry
// frames through the single transport-independent ingest path, so MQTT
// supports the same number/boolean/string values and descriptor rules as
// HTTP and CoAP.
func (h *PublishHook) OnPublished(cl *mqtt.Client, pk packets.Packet) {
	if commandID, ok := responseCommandID(pk.TopicName); ok {
		h.handleCommandResponse(cl.ID, commandID, pk.Payload)
		return
	}

	if pk.TopicName == mqttapi.TopicDeviceShadowReport {
		h.handleShadowReport(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayShadowReport {
		h.handleGatewayShadowReport(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayConnect {
		h.handleGatewayConnect(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayDisconnect {
		h.handleGatewayDisconnect(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayManifest {
		h.handleGatewayManifest(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayDiscovery {
		h.handleGatewayDiscovery(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayProbeResponse {
		h.handleGatewayProbeResponse(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayOTAProgress {
		h.handleGatewayOTAProgress(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayConnectorStatus {
		h.handleConnectorStatus(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewaySecretsStatus {
		h.handleSecretStatus(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayProfilesStatus {
		h.handleProfileStatus(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayOperationsResult {
		if h.opResults != nil {
			h.opResults.ReportResult(pk.Payload)
		}
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayProfiles {
		h.handleGatewayProfiles(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayHeartbeat {
		h.handleGatewayHeartbeat(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayGoodbye {
		h.handleGatewayGoodbye(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayLinkHealth {
		h.handleLinkEpisode(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayHello {
		h.handleHello(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayTelemetry {
		h.handleGatewayTelemetry(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayAttributes {
		h.handleGatewayAttributes(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayAttributesGet {
		h.handleGatewayAttributesGet(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicGatewayCommandResponse {
		h.handleGatewayCommandResponse(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicDeviceAttributes {
		h.handleDeviceAttributes(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicDeviceAttributesGet {
		h.handleDeviceAttributesGet(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName == mqttapi.TopicDeviceEvents {
		h.handleDeviceEvents(cl.ID, pk.Payload)
		return
	}
	if pk.TopicName != mqttapi.TopicDeviceTelemetry {
		return
	}

	current, ok := h.connections.connectionFor(cl.ID)
	if !ok {
		slog.Warn("unauthenticated publish reached publish hook",
			"client", cl.ID, "topic", pk.TopicName)
		return
	}
	identity := current.identity

	if h.deviceLimit != nil &&
		!h.deviceLimit.Allow(deviceKey(identity.TenantID, identity.DeviceID),
			time.Now().UTC()) {
		slog.Warn("device telemetry rate limit exceeded; frame dropped",
			"client", cl.ID, "device", identity.DeviceID)
		return
	}

	// Frame classification, descriptor application and Envelope production
	// all live in ingestshared; this ingress must not re-implement them.
	if _, err := sdktelemetry.Publish(context.Background(), h.js,
		h.resolver,
		sdktelemetry.Identity{
			TenantID: identity.TenantID,
			DeviceID: identity.DeviceID,
		}, contract.ProtocolMQTT, pk.Payload, time.Now().UTC(),
		sdktelemetry.WithEncoder(h.encoder),
		sdktelemetry.WithMediaStore(h.mediaStore)); err != nil {
		slog.Warn("mqtt telemetry publish failed",
			"client", cl.ID, "error", err)
		return
	}
	// Accounting runs only after a successful publish so the sampled view
	// reflects traffic the platform actually accepted, and only for device
	// telemetry: gateway control traffic is not tenant load.
	if h.accounting != nil {
		h.accounting.Observe(accounting.Event{
			TenantID: identity.TenantID,
			DeviceID: identity.DeviceID,
			RemoteIP: clientRemoteIP(cl),
			Protocol: contract.ProtocolMQTT,
			Topic:    pk.TopicName,
			Bytes:    len(pk.Payload),
		})
	}
}

// clientRemoteIP extracts the bare address from an MQTT client connection
// so the accounting "ip" dimension groups by host rather than by ephemeral
// port. It returns "" when the address cannot be parsed, and accounting
// then ignores the dimension instead of inventing a placeholder.
func clientRemoteIP(cl *mqtt.Client) string {
	addr := cl.Net.Remote
	if addr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func (h *PublishHook) handleShadowReport(clientID string, payload []byte) {
	current, ok := h.connections.connectionFor(clientID)
	if !ok {
		slog.Warn("unauthenticated shadow report reached publish hook", "client", clientID)
		return
	}
	var patch shadow.ReportedPatch
	if err := json.Unmarshal(payload, &patch); err != nil {
		slog.Warn("invalid shadow report", "client", clientID, "error", err)
		return
	}
	if h.shadows == nil {
		return
	}
	if _, err := h.shadows.UpdateReported(context.Background(), current.identity.DeviceID, patch); err != nil {
		slog.Warn("shadow report failed", "client", clientID, "error", err)
	}
}

// gatewayConnectPayload carries the sub-device attach announcement. The
// gateway authenticates with its own credential; each listed device must be
// a pre-registered device bound to the gateway.
type gatewayConnectPayload struct {
	Devices []string `json:"devices"`
}

// gatewayDisconnectPayload carries the sub-device detach announcement.
type gatewayDisconnectPayload struct {
	Devices []string `json:"devices"`
}

// handleGatewayConnect attaches the announced sub-devices to the gateway's
// presence session. Unauthorized or unknown devices are skipped with a log;
// one bad entry never blocks the rest of the batch.
func (h *PublishHook) handleGatewayConnect(clientID string, payload []byte) {
	current, ok := h.connections.connectionFor(clientID)
	if !ok {
		h.Log.Warn("unauthorized gateway connect reached publish hook", "client", clientID)
		return
	}
	identity := current.identity
	if !isGatewayIdentity(identity) {
		h.Log.Warn("non-gateway device published gateway connect", "client", clientID)
		return
	}
	if !current.negotiated {
		h.Log.Warn("gateway connect before hello; dropped", "client", clientID)
		return
	}
	var body gatewayConnectPayload
	if err := json.Unmarshal(payload, &body); err != nil {
		h.Log.Warn("invalid gateway connect payload", "client", clientID, "error", err)
		return
	}
	for _, deviceID := range body.Devices {
		ok, err := h.gateways.AssertGatewaySubdevice(context.Background(),
			identity.TenantID, identity.DeviceID, deviceID)
		if err != nil {
			h.Log.Warn("gateway sub-device assertion failed", "client", clientID,
				"device", deviceID, "error", err)
			continue
		}
		if !ok {
			h.Log.Warn("gateway not authorized for sub-device", "client", clientID,
				"device", deviceID)
			continue
		}
		if _, err := h.presence.Connect(context.Background(), presence.Identity{
			TenantID: identity.TenantID,
			DeviceID: deviceID,
		}, "", identity.DeviceID); err != nil {
			h.Log.Warn("sub-device presence connect failed", "device", deviceID, "error", err)
			continue
		}
		h.recordSubdevice(clientID, identity.TenantID, deviceID)
	}
}

// handleGatewayDisconnect removes the announced sub-devices from the
// gateway's presence session. The gateway itself stays online.
func (h *PublishHook) handleGatewayDisconnect(clientID string, payload []byte) {
	current, ok := h.connections.connectionFor(clientID)
	if !ok {
		return
	}
	identity := current.identity
	if !isGatewayIdentity(identity) {
		return
	}
	if !current.negotiated {
		return
	}
	var body gatewayDisconnectPayload
	if err := json.Unmarshal(payload, &body); err != nil {
		h.Log.Warn("invalid gateway disconnect payload", "client", clientID, "error", err)
		return
	}
	for _, deviceID := range body.Devices {
		// Sub-device sessions were started with an empty fencing session
		// id (the gateway owns them), so presence.Disconnect would be a
		// no-op. Use the gateway-scoped primitive, which ends presence
		// only while it is still attached to this gateway.
		if _, err := h.presence.EnsureGatewayOffline(context.Background(),
			presence.Identity{
				TenantID: identity.TenantID,
				DeviceID: deviceID,
			}, identity.DeviceID, presence.ReasonSession); err != nil {
			h.Log.Warn("sub-device presence disconnect failed", "device", deviceID, "error", err)
			continue
		}
		h.dropSubdevice(clientID, identity.TenantID, deviceID)
	}
}

// recordSubdevice tracks a gateway-attached sub-device for cascade offline.
func (h *PublishHook) recordSubdevice(clientID, tenantID, deviceID string) {
	h.connections.recordSubdevice(clientID, tenantID, deviceID)
}

// dropSubdevice untracks a detached sub-device.
func (h *PublishHook) dropSubdevice(clientID, tenantID, deviceID string) {
	h.connections.dropSubdevice(clientID, tenantID, deviceID)
}

// gatewayManifestFrame is the manifest uplink payload. It carries the
// gateway's full current online sub-device list aligned to an absolute
// wall-clock boundary (ADR-0026).
type gatewayManifestFrame = manifest.Frame

// handleGatewayManifest reconciles the gateway's full sub-device list. The
// gateway itself authenticates; reconciliation is transport-independent and
// owned by the manifest package, so this handler only gates identity and
// negotiation and decodes the frame. A malformed frame or a reconciler
// failure is logged and dropped, never echoed to the device.
func (h *PublishHook) handleGatewayManifest(clientID string, payload []byte) {
	if h.manifests == nil {
		return
	}
	current, ok := h.connections.connectionFor(clientID)
	if !ok {
		h.Log.Warn("unauthorized gateway manifest reached publish hook", "client", clientID)
		return
	}
	identity := current.identity
	if !isGatewayIdentity(identity) {
		h.Log.Warn("non-gateway device published gateway manifest", "client", clientID)
		return
	}
	if !current.negotiated {
		h.Log.Warn("gateway manifest before hello; dropped", "client", clientID)
		return
	}
	var frame gatewayManifestFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		h.Log.Warn("invalid gateway manifest payload", "client", clientID, "error", err)
		return
	}
	report, err := h.manifests.Reconcile(context.Background(),
		identity.TenantID, identity.DeviceID, frame)
	if err != nil {
		h.Log.Warn("gateway manifest reconcile failed",
			"client", clientID, "error", err)
		return
	}
	// The connector segment is reconciled by the connector service when the
	// edge actually advertises it; an absent field (older edge) is skipped so
	// its existing instances are not falsely classified as missing.
	if h.connectorManifests != nil && frame.Connectors != nil {
		reported := make([]connector.ReportedInstance, 0, len(*frame.Connectors))
		for _, c := range *frame.Connectors {
			reported = append(reported, connector.ReportedInstance{
				SpecID:     c.SpecID,
				Revision:   c.Revision,
				ConfigHash: c.ConfigHash,
			})
		}
		crep, cerr := h.connectorManifests.ReconcileReported(
			context.Background(), identity.TenantID, identity.DeviceID, reported)
		if cerr != nil {
			h.Log.Warn("connector manifest reconcile failed",
				"client", clientID, "error", cerr)
		} else if crep.Drift() {
			h.Log.Info("connector manifest drift",
				"client", clientID,
				"missing", len(crep.Missing),
				"stale", len(crep.Stale),
				"drifted", len(crep.Drifted),
				"extra", len(crep.Extra))
		}
	}
	// The optional full-replacement models segment (ADR-0039) is reconciled
	// only when the field is present: an absent field (older/unsupported
	// edge) leaves stored inventory untouched, while an explicit empty list
	// is an authoritative zero-inventory report that prunes every row.
	if h.modelInventory != nil && frame.Models != nil {
		models := make([]modelinventory.Input, 0, len(*frame.Models))
		for _, m := range *frame.Models {
			models = append(models, modelinventory.Input{
				ID:             m.ID,
				Version:        m.Version,
				Revision:       m.Revision,
				Task:           m.Task,
				Family:         m.Family,
				Backend:        m.Backend,
				ServerFamily:   m.ServerFamily,
				ModelName:      m.ModelName,
				State:          m.State,
				Reason:         m.Reason,
				Labels:         m.Labels,
				ActiveBindings: m.ActiveBindings,
			})
		}
		if _, err := h.modelInventory.Reconcile(
			context.Background(), identity.TenantID, identity.DeviceID,
			models, time.Now().UTC()); err != nil {
			h.Log.Warn("model manifest reconcile failed",
				"client", clientID, "error", err)
		}
	}
	// The manifest is the authoritative full online list; converge the
	// local authorization set to the in-sync devices so telemetry for a
	// manifest-promoted device is accepted and an omitted device is gated
	// off immediately (without waiting for presence to age out).
	h.connections.replaceSubdevices(clientID, identity.TenantID, report.Class.InSync)
	if report.Class.Drift() {
		h.Log.Info("gateway manifest drift",
			"client", clientID,
			"missing", len(report.Class.Missing),
			"unregistered", len(report.Class.Unregistered))
	}
}

// handleConnectorStatus consumes an edge-reported connector lifecycle
// frame (ADR-0026). It gates gateway identity and negotiation, then hands the
// transport-independent frame to the connector service. Malformed frames and
// references to unknown specs are logged and dropped; the service never
// echoes errors to the edge on this fire-and-forget topic.
func (h *PublishHook) handleConnectorStatus(clientID string, payload []byte) {
	if h.connectors == nil {
		return
	}
	current, ok := h.connections.connectionFor(clientID)
	if !ok || !isGatewayIdentity(current.identity) || !current.negotiated {
		return
	}
	var frame connector.StatusFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		h.Log.Warn("invalid connector status payload", "client", clientID, "error", err)
		return
	}
	if _, err := h.connectors.HandleStatus(context.Background(),
		current.identity.TenantID, frame); err != nil {
		h.Log.Warn("connector status handling failed",
			"client", clientID, "spec", frame.SpecID, "error", err)
	}
}

// handleSecretStatus consumes an edge-reported secret delivery frame
// (ADR-0034). It gates gateway identity and negotiation, then hands the
// transport-independent frame to the binding service. Malformed frames are
// logged and dropped; the topic is fire-and-forget.
func (h *PublishHook) handleSecretStatus(clientID string, payload []byte) {
	if h.secretBindings == nil {
		return
	}
	current, ok := h.connections.connectionFor(clientID)
	if !ok || !isGatewayIdentity(current.identity) || !current.negotiated {
		return
	}
	var frame secretbinding.StatusFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		h.Log.Warn("invalid secret status payload", "client", clientID, "error", err)
		return
	}
	if err := h.secretBindings.HandleStatus(context.Background(),
		current.identity.TenantID, current.identity.DeviceID, frame); err != nil {
		h.Log.Warn("secret status handling failed", "client", clientID, "error", err)
	}
}

// handleProfileStatus consumes an edge-reported profile delivery frame
// (ADR-0038). It gates gateway identity and negotiation, then hands the
// transport-independent frame to the delivery service. Malformed frames are
// logged and dropped; the topic is fire-and-forget.
func (h *PublishHook) handleProfileStatus(clientID string, payload []byte) {
	if h.profileBindings == nil {
		return
	}
	current, ok := h.connections.connectionFor(clientID)
	if !ok || !isGatewayIdentity(current.identity) || !current.negotiated {
		return
	}
	var frame profilepush.StatusFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		h.Log.Warn("invalid profile status payload", "client", clientID, "error", err)
		return
	}
	if err := h.profileBindings.HandleStatus(context.Background(),
		current.identity.TenantID, current.identity.DeviceID, frame); err != nil {
		h.Log.Warn("profile status handling failed", "client", clientID, "error", err)
	}
}

// handleGatewayProfiles caches EdgeX device profiles reported on
// v1/gateway/profiles. Like connector status it gates gateway identity and
// negotiation; tenant and gateway come exclusively from the authenticated
// session, so a frame cannot inject profiles into another tenant or gateway.
// The topic is fire-and-forget: malformed frames and cache failures are logged
// and dropped rather than echoed to the edge.
func (h *PublishHook) handleGatewayProfiles(clientID string, payload []byte) {
	if h.edgeProfiles == nil {
		return
	}
	current, ok := h.connections.connectionFor(clientID)
	if !ok || !isGatewayIdentity(current.identity) || !current.negotiated {
		h.Log.Warn("gateway profiles report from invalid session; dropped",
			"client", clientID, "known", ok)
		return
	}
	var frame edgeprofile.Frame
	if err := json.Unmarshal(payload, &frame); err != nil {
		h.Log.Warn("invalid gateway profiles payload", "client", clientID, "error", err)
		return
	}
	if len(frame.Profiles) == 0 {
		return
	}
	if err := h.edgeProfiles.Ingest(context.Background(),
		current.identity.TenantID, current.identity.DeviceID, frame); err != nil {
		h.Log.Warn("gateway edge profile ingest failed",
			"client", clientID, "count", len(frame.Profiles), "error", err)
	}
}

// gatewayHeartbeatFrame is the application liveness ping on
// `v1/gateway/heartbeat`. It is independent of the MQTT transport keepalive.
type gatewayHeartbeatFrame = linkhealth.Frame

// handleGatewayHeartbeat feeds the ADR-0026 link watchdog. It only gates
// identity and negotiation; the watchdog is advisory and never blocks or
// echoes to the edge.
func (h *PublishHook) handleGatewayHeartbeat(clientID string, payload []byte) {
	if h.linkHealth == nil {
		return
	}
	current, ok := h.connections.connectionFor(clientID)
	if !ok || !isGatewayIdentity(current.identity) || !current.negotiated {
		return
	}
	// Payload is optional: an empty heartbeat still refreshes the lease.
	var frame gatewayHeartbeatFrame
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &frame); err != nil {
			h.Log.Warn("invalid gateway heartbeat payload", "client", clientID, "error", err)
			return
		}
	}
	h.linkHealth.ObserveHeartbeat(context.Background(),
		current.identity.TenantID, current.identity.DeviceID, time.Now().UTC())
}

// gatewayGoodbyeFrame is the graceful-shutdown notice (and MQTT LWT payload
// topic) on `v1/gateway/goodbye`.
type gatewayGoodbyeFrame = linkhealth.Goodbye

// handleGatewayGoodbye records a planned or unexpected shutdown in the link
// watchdog. The broker also fires OnDisconnect for the closing connection;
// the watchdog dedupes a goodbye followed by that transport close.
func (h *PublishHook) handleGatewayGoodbye(clientID string, payload []byte) {
	if h.linkHealth == nil {
		return
	}
	current, ok := h.connections.connectionFor(clientID)
	if !ok || !isGatewayIdentity(current.identity) {
		return
	}
	var frame gatewayGoodbyeFrame
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &frame); err != nil {
			h.Log.Warn("invalid gateway goodbye payload", "client", clientID, "error", err)
			return
		}
	}
	h.linkHealth.ObserveGoodbye(context.Background(),
		current.identity.TenantID, current.identity.DeviceID,
		frame.Planned, frame.Reason, time.Now().UTC())
}

// gatewayTelemetryFrame is the batch uplink produced by a gateway on
// `v1/gateway/telemetry`. The outer map keys are sub-device IDs; each entry
// is a telemetry frame that may carry an optional `ts` field in
// milliseconds, mirroring the ThingsBoard gateway contract.
type gatewayTelemetryFrame map[string]map[string]json.RawMessage

// telemetryTS is the reserved per-frame timestamp key.
const telemetryTS = "ts"

// handleGatewayTelemetry forwards a gateway batch telemetry frame to the
// shared SDK uplink path, one Envelope per measurement per sub-device. Only
// sub-devices attached by this connection are accepted, so a gateway cannot
// write on behalf of a device it never claimed. `ts` is stripped before
// classification and used as the event time when present.
func (h *PublishHook) handleGatewayTelemetry(clientID string, payload []byte) {
	current, ok := h.connections.connectionFor(clientID)
	if !ok {
		h.Log.Warn("unauthorized gateway telemetry reached publish hook", "client", clientID)
		return
	}
	identity := current.identity
	if !isGatewayIdentity(identity) {
		h.Log.Warn("non-gateway device published gateway telemetry", "client", clientID)
		return
	}
	if !current.negotiated {
		h.Log.Warn("gateway telemetry before hello; dropped", "client", clientID)
		return
	}
	var frame gatewayTelemetryFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		h.Log.Warn("invalid gateway telemetry payload", "client", clientID, "error", err)
		return
	}
	for deviceID, values := range frame {
		if !h.connections.isSubdevice(clientID, identity.TenantID, deviceID) {
			h.Log.Warn("gateway telemetry for un-attached sub-device dropped",
				"client", clientID, "device", deviceID)
			continue
		}
		if h.deviceLimit != nil &&
			!h.deviceLimit.Allow(deviceKey(identity.TenantID, deviceID),
				time.Now().UTC()) {
			h.Log.Warn("sub-device telemetry rate limit exceeded; frame dropped",
				"client", clientID, "device", deviceID)
			continue
		}
		eventTime := time.Now().UTC()
		if rawTS, ok := values[telemetryTS]; ok {
			var tsMillis int64
			if err := json.Unmarshal(rawTS, &tsMillis); err == nil {
				eventTime = time.UnixMilli(tsMillis).UTC()
			}
			delete(values, telemetryTS)
		}
		cleaned, err := json.Marshal(values)
		if err != nil {
			h.Log.Warn("gateway telemetry frame re-encode failed",
				"client", clientID, "device", deviceID, "error", err)
			continue
		}
		if _, err := sdktelemetry.Publish(context.Background(), h.js,
			h.resolver, sdktelemetry.Identity{
				TenantID: identity.TenantID,
				DeviceID: deviceID,
			}, contract.ProtocolMQTT, cleaned, eventTime,
			sdktelemetry.WithEncoder(h.encoder),
			sdktelemetry.WithMediaStore(h.mediaStore)); err != nil {
			h.Log.Warn("gateway sub-device telemetry publish failed",
				"client", clientID, "device", deviceID, "error", err)
		}
	}
}

// clientAttributeScope is the device-owned attribute scope (ADR-0011). It
// mirrors the literal used by the rule engine's Save Attributes node; devices
// may only ever upsert this scope, never SERVER_SCOPE/SHARED_SCOPE.
const clientAttributeScope = "CLIENT"

// handleDeviceAttributes persists a directly connected device's client-scope
// attribute report. The frame is a flat {key: value} object and the target is
// always the authenticated device itself, so no sub-device authorization is
// involved.
func (h *PublishHook) handleDeviceAttributes(clientID string, payload []byte) {
	current, ok := h.connections.connectionFor(clientID)
	if !ok {
		h.Log.Warn("unauthenticated attribute report reached publish hook", "client", clientID)
		return
	}
	var attrs map[string]any
	if err := json.Unmarshal(payload, &attrs); err != nil {
		h.Log.Warn("invalid device attributes payload", "client", clientID, "error", err)
		return
	}
	if h.attributes == nil || len(attrs) == 0 {
		return
	}
	identity := current.identity
	if err := h.attributes.SaveAttributes(context.Background(),
		identity.TenantID, identity.DeviceID, clientAttributeScope, attrs); err != nil {
		h.Log.Warn("device attribute upsert failed", "client", clientID, "error", err)
	}
}

// handleDeviceEvents persists one or more domain events from a directly
// connected device. The frame is either a single event object or an array of
// them. The target is always the authenticated device itself; event content is
// tenant-defined and opaque to the platform.
func (h *PublishHook) handleDeviceEvents(clientID string, payload []byte) {
	current, ok := h.connections.connectionFor(clientID)
	if !ok {
		h.Log.Warn("unauthenticated event report reached publish hook", "client", clientID)
		return
	}
	var frames []deviceevent.Frame
	// Accept both a single object and a JSON array: many devices emit one
	// event per publish, gateways/batched clients emit several.
	if trimmed := firstNonSpace(payload); trimmed == '[' {
		if err := json.Unmarshal(payload, &frames); err != nil {
			h.Log.Warn("invalid device events payload", "client", clientID, "error", err)
			return
		}
	} else {
		var one deviceevent.Frame
		if err := json.Unmarshal(payload, &one); err != nil {
			h.Log.Warn("invalid device event payload", "client", clientID, "error", err)
			return
		}
		frames = []deviceevent.Frame{one}
	}
	if h.events == nil {
		h.Log.Warn("device events uplink received but no event ingester is wired; frame dropped",
			"client", clientID)
		return
	}
	if len(frames) == 0 {
		return
	}
	identity := current.identity
	if err := h.events.Ingest(context.Background(),
		identity.TenantID, identity.DeviceID, frames); err != nil {
		h.Log.Warn("device event ingest failed", "client", clientID, "error", err)
	}
}

// firstNonSpace returns the first non-whitespace byte of payload, or 0 when
// empty. It lets the event handler distinguish an array from an object without
// fully decoding twice.
func firstNonSpace(payload []byte) byte {
	for _, b := range payload {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return b
		}
	}
	return 0
}

// handleGatewayAttributes persists client-scope attributes for sub-devices.
// The frame is {device: {key: value}}. Every named device must be attached to
// and authorized for this gateway; an unattached or unauthorized entry is
// skipped without failing the rest of the batch.
func (h *PublishHook) handleGatewayAttributes(clientID string, payload []byte) {
	current, ok := h.connections.connectionFor(clientID)
	if !ok {
		h.Log.Warn("unauthorized gateway attributes reached publish hook", "client", clientID)
		return
	}
	identity := current.identity
	if !isGatewayIdentity(identity) {
		h.Log.Warn("non-gateway device published gateway attributes", "client", clientID)
		return
	}
	if !current.negotiated {
		h.Log.Warn("gateway attributes before hello; dropped", "client", clientID)
		return
	}
	var frame map[string]map[string]any
	if err := json.Unmarshal(payload, &frame); err != nil {
		h.Log.Warn("invalid gateway attributes payload", "client", clientID, "error", err)
		return
	}
	if h.attributes == nil {
		return
	}
	for deviceID, attrs := range frame {
		if !h.connections.isSubdevice(clientID, identity.TenantID, deviceID) {
			h.Log.Warn("gateway attributes for un-attached sub-device dropped",
				"client", clientID, "device", deviceID)
			continue
		}
		if len(attrs) == 0 {
			continue
		}
		if err := h.attributes.SaveAttributes(context.Background(),
			identity.TenantID, deviceID, clientAttributeScope, attrs); err != nil {
			h.Log.Warn("gateway sub-device attribute upsert failed",
				"client", clientID, "device", deviceID, "error", err)
		}
	}
}

// handleGatewayCommandResponse correlates a sub-device command result a
// gateway publishes on v1/gateway/command/response. The frame carries the
// target sub-device explicitly; it is accepted only from a negotiated gateway
// for a sub-device attached to that connection, so a gateway cannot inject a
// result for a device another gateway owns.
func (h *PublishHook) handleGatewayCommandResponse(clientID string, payload []byte) {
	current, ok := h.connections.connectionFor(clientID)
	if !ok {
		h.Log.Warn("unauthenticated gateway command response reached publish hook",
			"client", clientID)
		return
	}
	identity := current.identity
	if !isGatewayIdentity(identity) {
		h.Log.Warn("non-gateway device published gateway command response", "client", clientID)
		return
	}
	if !current.negotiated {
		h.Log.Warn("gateway command response before hello; dropped", "client", clientID)
		return
	}
	var frame gatewayCommandResponseFrame
	if err := json.Unmarshal(payload, &frame); err != nil {
		h.Log.Warn("invalid gateway command response payload", "client", clientID, "error", err)
		return
	}
	if frame.CommandID == "" || frame.Device == "" {
		h.Log.Warn("gateway command response missing command_id or device", "client", clientID)
		return
	}
	if !h.connections.isSubdevice(clientID, identity.TenantID, frame.Device) {
		h.Log.Warn("gateway command response for un-attached sub-device dropped",
			"client", clientID, "device", frame.Device)
		return
	}
	if h.responses == nil {
		return
	}
	h.responses.ReportResponse(identity.TenantID, frame.Device,
		frame.CommandID, frame.Data)
}

// handleCommandResponse forwards an uplink response to the relay for
// correlation. The response connection must still resolve to an identity.
func (h *PublishHook) handleCommandResponse(clientID, commandID string, payload []byte) {
	current, ok := h.connections.connectionFor(clientID)
	if !ok {
		h.Log.Warn("unauthenticated command response reached publish hook",
			"client", clientID, "command", commandID)
		return
	}
	if h.responses == nil {
		return
	}
	h.responses.ReportResponse(
		current.identity.TenantID, current.identity.DeviceID,
		commandID, payload)
}

// Server owns the embedded broker and its lifecycle.
type Server struct {
	broker         *mqtt.Server
	authHook       *AuthHook
	publishHook    *PublishHook
	commandRelay   *CommandRelay
	operationRelay *OperationRelay
}

// NewServer constructs the broker, registers hooks and a TCP listener.
// When tlsAddr and tlsConfig are both set, an additional mTLS listener
// is attached; passing a config without an address keeps plain TCP only.
func NewServer(addr, tlsAddr, ingressID string, tlsConfig *tls.Config, nc *nats.Conn, auth Authenticator, presence PresenceManager, js jetstream.JetStream, resolver MeasurementResolver, shadowReporter ShadowReporter) (*Server, error) {
	broker := mqtt.New(nil)

	authHook := NewAuthHook(auth, presence, ingressID)
	if err := broker.AddHook(authHook, nil); err != nil {
		return nil, fmt.Errorf("register auth hook: %w", err)
	}

	relay := NewCommandRelay(nc, broker, authHook)
	publishHook := NewPublishHook(js, authHook, presence, relay, resolver)
	publishHook.SetShadowReporter(shadowReporter)

	helloEnforcer := newHelloEnforcer(broker, authHook, edge.DefaultConfig().HelloTimeout)
	publishHook.SetBroker(broker, helloEnforcer)
	authHook.SetGatewayConnectCallback(helloEnforcer.arm)

	if err := broker.AddHook(publishHook, nil); err != nil {
		return nil, fmt.Errorf("register publish hook: %w", err)
	}
	if err := relay.Subscribe(ingressID); err != nil {
		return nil, fmt.Errorf("subscribe command dispatch: %w", err)
	}
	probeRelay := NewProbeRelay(nc, broker, authHook)
	publishHook.SetProbeReporter(probeRelay)
	if err := probeRelay.Subscribe(ingressID); err != nil {
		return nil, fmt.Errorf("subscribe probe dispatch: %w", err)
	}
	otaRelay := NewOTARelay(nc, broker, authHook)
	publishHook.SetOTAReporter(otaRelay)
	if err := otaRelay.Subscribe(ingressID); err != nil {
		return nil, fmt.Errorf("subscribe ota dispatch: %w", err)
	}
	discoveryRelay := NewDiscoveryRelay(nc, broker, authHook)
	if err := discoveryRelay.Subscribe(ingressID); err != nil {
		return nil, fmt.Errorf("subscribe discovery decision dispatch: %w", err)
	}
	operationRelay := NewOperationRelay(nc, broker, authHook)
	publishHook.SetOperationResultReporter(operationRelay)
	if err := operationRelay.Subscribe(ingressID); err != nil {
		return nil, fmt.Errorf("subscribe operation dispatch: %w", err)
	}
	tcp := listeners.NewTCP(listeners.Config{ID: contract.ListenerTCPID, Address: addr})
	if err := broker.AddListener(tcp); err != nil {
		return nil, fmt.Errorf("register tcp listener: %w", err)
	}

	if tlsAddr != "" && tlsConfig != nil {
		tlsListener := listeners.NewTCP(listeners.Config{
			ID:        contract.ListenerTLSID,
			Address:   tlsAddr,
			TLSConfig: tlsConfig,
		})
		if err := broker.AddListener(tlsListener); err != nil {
			return nil, fmt.Errorf("register tls listener: %w", err)
		}
	}

	return &Server{broker: broker, authHook: authHook, publishHook: publishHook, commandRelay: relay, operationRelay: operationRelay}, nil
}

// SetOperationSecretResolver wires last-mile secret:// resolution into the
// operation invoke relay (ADR-0035).
func (s *Server) SetOperationSecretResolver(resolver ConnectorSecretResolver) {
	s.operationRelay.SetSecretResolver(resolver)
}

// SetConnectorSecretResolver wires last-mile secret:// resolution into the
// connector desired-spec relay (migration 0047).
func (s *Server) SetConnectorSecretResolver(resolver ConnectorSecretResolver) {
	s.commandRelay.SetSecretResolver(resolver)
}

// SetDeliverySecretResolver wires last-mile plaintext substitution into
// the secret delivery relay (ADR-0034).
func (s *Server) SetDeliverySecretResolver(resolver DeliverySecretResolver, marker SecretDispatchMarker) {
	s.commandRelay.SetDeliverySecretResolver(resolver, marker)
}

// SetProfileDispatchMarker wires DISPATCHED marking into the profile
// delivery relay (ADR-0038).
func (s *Server) SetProfileDispatchMarker(marker ProfileDispatchMarker) {
	s.commandRelay.SetProfileDispatchMarker(marker)
}

// SetRevocationChecker wires CRL revocation checking of mTLS client
// certificates (ADR-0009) into the authentication hook.
func (s *Server) SetRevocationChecker(checker RevocationChecker) {
	s.authHook.SetRevocationChecker(checker)
}

// Run starts serving and blocks until ctx is canceled, after which the
// broker is closed gracefully.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() { errCh <- s.broker.Serve() }()

	select {
	case <-ctx.Done():
		return s.broker.Close()
	case err := <-errCh:
		return err
	}
}

// ClientCount reports the number of currently connected MQTT clients.
func (s *Server) ClientCount() int {
	return s.broker.Clients.Len()
}

// SetGatewayManager wires sub-device authorization for the publish hook.
// The registry admin is constructed after the ingress in main, so the
// wiring is deferred through this setter.
func (s *Server) SetGatewayManager(manager GatewayManager) {
	s.publishHook.SetGatewayManager(manager)
}

// SetAttributeSaver wires device-reported CLIENT-scope attribute uplinks.
func (s *Server) SetAttributeSaver(saver AttributeSaver) {
	s.publishHook.SetAttributeSaver(saver)
}

// SetAttributeFetcher wires on-demand device/gateway pulls of
// platform-managed attributes.
func (s *Server) SetAttributeFetcher(fetcher AttributeFetcher) {
	s.publishHook.SetAttributeFetcher(fetcher)
}

// SetShadowAuditor wires audit recording of rejected gateway shadow entries.
func (s *Server) SetShadowAuditor(auditor shadowReportAuditor) {
	s.publishHook.SetShadowAuditor(auditor)
}

// SetEventIngester wires device domain-event uplinks.
func (s *Server) SetEventIngester(ingester EventIngester) {
	s.publishHook.SetEventIngester(ingester)
}

// SetManifestReconciler wires manifest reconciliation into the publish hook.
func (s *Server) SetManifestReconciler(reconciler ManifestReconciler) {
	s.publishHook.SetManifestReconciler(reconciler)
}

// SetConnectorStatusReceiver wires connector status uplinks into the
// connector lifecycle service.
func (s *Server) SetConnectorStatusReceiver(receiver ConnectorStatusReceiver) {
	s.publishHook.SetConnectorStatusReceiver(receiver)
}

// SetConnectorRedriver wires the post-hello desired-spec re-send.
func (s *Server) SetConnectorRedriver(redriver ConnectorRedriver) {
	s.publishHook.SetConnectorRedriver(redriver)
}

// SetSecretStatusReceiver wires secret delivery status uplinks into the
// binding service.
func (s *Server) SetSecretStatusReceiver(receiver SecretStatusReceiver) {
	s.publishHook.SetSecretStatusReceiver(receiver)
}

// SetSecretRedriver wires the post-hello desired-secret re-send.
func (s *Server) SetSecretRedriver(redriver SecretRedriver) {
	s.publishHook.SetSecretRedriver(redriver)
}

// SetProfileStatusReceiver wires profile delivery status uplinks into the
// downlink service (ADR-0038).
func (s *Server) SetProfileStatusReceiver(receiver ProfileStatusReceiver) {
	s.publishHook.SetProfileStatusReceiver(receiver)
}

// SetProfileRedriver wires the post-hello desired-profile re-send.
func (s *Server) SetProfileRedriver(redriver ProfileRedriver) {
	s.publishHook.SetProfileRedriver(redriver)
}

// SetConnectorManifestReconciler wires connector manifest reconciliation.
func (s *Server) SetConnectorManifestReconciler(reconciler ConnectorManifestReconciler) {
	s.publishHook.SetConnectorManifestReconciler(reconciler)
}

// SetModelInventoryReconciler wires the models segment of gateway manifest
// reconciliation (ADR-0039).
func (s *Server) SetModelInventoryReconciler(reconciler ModelInventoryReconciler) {
	s.publishHook.SetModelInventoryReconciler(reconciler)
}

// SetConnectorSchemaRegistrar wires edge-advertised config-schema
// registration from hello (ADR-0026 schema validation slice).
func (s *Server) SetConnectorSchemaRegistrar(registrar EdgeConnectorSchemaRegistrar) {
	s.publishHook.SetConnectorSchemaRegistrar(registrar)
}

// SetSecretTypeRegistrar wires edge-advertised secret type descriptor
// registration from hello (ADR-0034).
func (s *Server) SetSecretTypeRegistrar(registrar EdgeSecretTypeRegistrar) {
	s.publishHook.SetSecretTypeRegistrar(registrar)
}

// SetOperationRegistrar wires edge-advertised gateway operation
// registration from hello (ADR-0035).
func (s *Server) SetOperationRegistrar(registrar EdgeOperationRegistrar) {
	s.publishHook.SetOperationRegistrar(registrar)
}

// SetDiscoveryReceiver wires gateway auto-provisioning uplinks and their
// result downlink into the discovery service.
func (s *Server) SetDiscoveryReceiver(receiver DiscoveryReceiver) {
	s.publishHook.SetDiscoveryReceiver(receiver)
}

// SetEdgeProfileReceiver wires edge-reported EdgeX profile uplinks into the
// edge profile cache/adopt service.
func (s *Server) SetEdgeProfileReceiver(receiver EdgeProfileReceiver) {
	s.publishHook.SetEdgeProfileReceiver(receiver)
}

// SetLinkHealthObserver wires the ADR-0026 link watchdog into both the
// transport disconnect path and the uplink (hello/heartbeat/goodbye) path.
func (s *Server) SetLinkHealthObserver(observer LinkHealthObserver) {
	s.authHook.SetLinkHealthObserver(observer)
	s.publishHook.SetLinkHealthObserver(observer)
}

// SetEdgeNegotiator wires hello negotiation into the publish hook.
func (s *Server) SetEdgeNegotiator(negotiator EdgeNegotiator) {
	s.publishHook.SetEdgeNegotiator(negotiator)
}

// SetLinkEpisodeStore wires edge-reported link episode application into
// the publish hook.
func (s *Server) SetLinkEpisodeStore(store LinkEpisodeStore) {
	s.publishHook.linkEpisodes = store
}

// SetEncoder wires bus compression into the publish hook.
func (s *Server) SetEncoder(encoder sdktelemetry.FrameEncoder) {
	s.publishHook.SetEncoder(encoder)
}

// SetMediaStore wires blob offload for marked frame fields into the
// publish hook.
func (s *Server) SetMediaStore(store sdktelemetry.MediaStore) {
	s.publishHook.SetMediaStore(store)
}

// SetDeviceLimit wires per-device rate limiting into the publish hook.
func (s *Server) SetDeviceLimit(limiter DeviceRateLimiter) {
	s.publishHook.SetDeviceLimit(limiter)
}

// SetAccounting wires resource accounting into the publish hook.
func (s *Server) SetAccounting(observer accounting.Observer) {
	s.publishHook.SetAccounting(observer)
}

// DisconnectDevice closes the live MQTT client of a device, if present.
// It is the lifecycle hook used after credential invalidation
// (decommission or credential-resetting transfer). Closing the client
// triggers OnDisconnect through the broker; presence was already force
// removed by the registry service.
func (s *Server) DisconnectDevice(ctx context.Context, tenantID, deviceID string) error {
	clientID, ok := s.authHook.clientIDFor(tenantID, deviceID)
	if !ok {
		return nil
	}
	cl, exists := s.broker.Clients.Get(clientID)
	if !exists {
		return nil
	}
	return s.broker.DisconnectClient(cl, packets.CodeDisconnect)
}
