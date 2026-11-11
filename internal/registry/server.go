// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package registry implements the RegistryAuth gRPC service.
//
// Registry is the system of record for devices and credentials. Southbound
// ingress services delegate authentication to this service instead of
// embedding identity knowledge; at 10M-device scale the hot path is stateless
// X.509/JWT verification plus the layered cache described in ADR-0009, while
// this service remains the authoritative fallback and management interface.
package registry

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/internal/mqttapi"
	"github.com/nereidai/nereidlink/internal/store"
)

// ConnectionPolicy enforces license or lifecycle restrictions before a
// new ingress connection is accepted.
type ConnectionPolicy interface {
	// AllowNewConnection returns an error when the tenant is restricted.
	AllowNewConnection(ctx context.Context, tenantID string) error
}

// Server implements v1.RegistryAuthServer.
type Server struct {
	v1.UnimplementedRegistryAuthServer

	// Pool is the shared PostgreSQL connection pool.
	Pool *pgxpool.Pool

	// policy is optional; nil preserves unit-test compatibility.
	policy ConnectionPolicy

	// keyResolver supplies remote JWK Sets for jwks_uri JWT credentials;
	// nil preserves unit-test compatibility and inline-key verification.
	keyResolver keyResolver
}

// SetConnectionPolicy wires the license/lifecycle connection gate.
func (s *Server) SetConnectionPolicy(policy ConnectionPolicy) {
	s.policy = policy
}

// SetKeyResolver wires the remote JWKS resolver used for device JWTs.
func (s *Server) SetKeyResolver(resolver keyResolver) {
	s.keyResolver = resolver
}

// NewServer constructs a registry service backed by pool.
func NewServer(pool *pgxpool.Pool) *Server {
	return &Server{Pool: pool}
}

// Authenticate verifies a device credential presented by an ingress node.
// Unknown credentials resolve to ok=false rather than a gRPC error so the
// caller can apply protocol-level rejection cleanly.
func (s *Server) Authenticate(ctx context.Context, req *v1.AuthenticateRequest) (*v1.AuthenticateResponse, error) {
	// Load the credential by its public identifier; the validity gates
	// (status, window, tenant/device state) are enforced inside the query.
	stored, err := store.LoadCredential(ctx, s.Pool, req.CredentialsId)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The overwhelming majority of these are clients that sent no
			// MQTT username at all, which is exactly what a pure-certificate
			// MQTT client does by default.
			logAuthRejection("unknown or inactive credential", req.CredentialsId,
				req.CertificateFingerprint, "")
			return &v1.AuthenticateResponse{Ok: false}, nil
		}
		return nil, err
	}

	// Prove possession of the presented secret using the credential
	// mechanism. Malformed presented material behaves like a wrong secret.
	ok, err := verifyCredential(ctx, stored, req.CredentialsValue, req.CertificateFingerprint, s.keyResolver)
	if err != nil {
		logAuthRejection("credential material could not be parsed",
			req.CredentialsId, req.CertificateFingerprint, stored.Type)
		return &v1.AuthenticateResponse{Ok: false}, nil
	}
	if !ok {
		reason := "proof of possession failed"
		if stored.Type == "X509" && req.CertificateFingerprint == "" {
			// No peer certificate means the client dialled the TLS port
			// without its certificate, or dialled the plain port.
			reason = "X509 credential without a client certificate"
		}
		logAuthRejection(reason, req.CredentialsId,
			req.CertificateFingerprint, stored.Type)
		return &v1.AuthenticateResponse{Ok: false}, nil
	}

	if s.policy != nil {
		if err := s.policy.AllowNewConnection(ctx, stored.TenantID); err != nil {
			logAuthRejection("connection policy denied",
				req.CredentialsId, req.CertificateFingerprint, stored.Type)
			return &v1.AuthenticateResponse{Ok: false}, nil
		}
	}

	// Quarantined devices keep their credentials on record but may
	// not open connections until the release. The loader already
	// excludes them; the check stays as defense in depth.
	var deviceState string
	var isGateway bool
	if err := s.Pool.QueryRow(ctx,
		`SELECT state, is_gateway FROM device WHERE id = $1 AND tenant_id = $2`,
		stored.DeviceID, stored.TenantID).Scan(&deviceState, &isGateway); err != nil {
		return nil, err
	}
	if deviceState == "QUARANTINED" {
		logAuthRejection("device is quarantined", req.CredentialsId,
			req.CertificateFingerprint, stored.Type)
		return &v1.AuthenticateResponse{Ok: false}, nil
	}

	device := &v1.DeviceIdentity{
		TenantId: stored.TenantID,
		DeviceId: stored.DeviceID,
	}
	if isGateway {
		device.Capabilities = []string{mqttapi.CapabilityGateway}
	}

	// Return the verification material so an ingress can cache the record
	// and prove possession locally on the next connection. Expiry bounds
	// the cache TTL; 0 means no expiry.
	record := &v1.CredentialRecord{
		Type:             stored.Type,
		CredentialsValue: stored.CredentialsValue,
		Extra:            stored.Extra,
		CredentialEpoch:  stored.CredentialEpoch,
	}
	if stored.ExpiresAt != nil {
		record.ExpiresAtUnix = stored.ExpiresAt.Unix()
	}
	return &v1.AuthenticateResponse{
		Ok:     true,
		Device: device,
		Record: record,
	}, nil
}

// logAuthRejection records why a device connection was refused. Every
// rejection looks identical on the wire (the client only sees a CONNACK
// refusal), which used to leave operators with no way to tell a wrong
// username from an unregistered certificate. The credential id, the
// mechanism and whether a certificate was presented are operator-visible
// facts; no secret, private key or fingerprint is logged.
func logAuthRejection(reason, credentialsID, fingerprint, credentialType string) {
	attrs := []any{"reason", reason, "credentials_id", credentialsID}
	if credentialType != "" {
		attrs = append(attrs, "type", credentialType)
	}
	if fingerprint != "" {
		attrs = append(attrs, "client_certificate", "presented")
	} else {
		attrs = append(attrs, "client_certificate", "absent")
	}
	slog.Warn("device authentication rejected", attrs...)
}

// Authorize reports whether a device may perform an action on a topic.
//
// Capabilities are resolved server-side and never taken from the request.
// That distinction is the whole point of this RPC: it exists so an external
// ingress connector (ADR-0008) can ask the platform instead of deciding for
// itself. A caller that could assert its own capabilities would make the
// check a no-op, so DeviceIdentity.capabilities in the request is ignored
// outright rather than trusted.
//
// Authorization is fail-closed at three layers: an unparseable request, a
// device that does not exist in the caller's tenant, and a quarantined device
// all yield Allowed: false. Only the topic rules in mqttapi.Authorize can
// grant access, and the matching is done live against the frozen templates
// rather than cached per full topic (ADR-0009).
func (s *Server) Authorize(ctx context.Context, req *v1.AuthorizeRequest) (*v1.AuthorizeResponse, error) {
	device := req.GetDevice()
	if device == nil {
		return &v1.AuthorizeResponse{Allowed: false}, nil
	}
	topic, action := req.GetTopic(), req.GetAction()
	if topic == "" || action == "" {
		return &v1.AuthorizeResponse{Allowed: false}, nil
	}

	// Confirm the device exists in the tenant the caller claims. The lookup
	// doubles as UUID validation: a malformed identifier errors here and is
	// denied rather than passed through to the topic rules.
	var state string
	err := s.Pool.QueryRow(ctx,
		`SELECT state FROM device WHERE id = $1 AND tenant_id = $2`,
		device.GetDeviceId(), device.GetTenantId()).Scan(&state)
	if err != nil {
		// An unknown device is a caller bug or a stale credential; log it
		// because it is never a normal denied publish.
		slog.Warn("device authorization denied", "reason", "unknown device",
			"tenant_id", device.GetTenantId(), "device_id", device.GetDeviceId())
		return &v1.AuthorizeResponse{Allowed: false}, nil
	}
	// A quarantined device must not gain topic access even if a credential
	// is still presented; quarantine outranks authorization.
	if state == "QUARANTINED" {
		return &v1.AuthorizeResponse{Allowed: false}, nil
	}

	// Capabilities are derived from the persisted device record, never from
	// the caller's claimed capabilities (L-025). Today the only capability
	// is "gateway", granted by the is_gateway device flag; absent a record
	// the set stays empty and the gateway topic set fails closed.
	var isGateway bool
	capabilities := []string{}
	err = s.Pool.QueryRow(ctx,
		`SELECT is_gateway FROM device WHERE id = $1 AND tenant_id = $2`,
		device.GetDeviceId(), device.GetTenantId()).Scan(&isGateway)
	if err == nil && isGateway {
		capabilities = append(capabilities, mqttapi.CapabilityGateway)
	}
	if err != nil {
		slog.Warn("device capability lookup failed", "error", err)
	}

	return &v1.AuthorizeResponse{
		Allowed: mqttapi.Authorize(action, topic, capabilities),
	}, nil
}
