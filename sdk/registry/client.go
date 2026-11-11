// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package registry provides the public registry gRPC client used by
// southbound adapters to authenticate device credentials. It is
// self-contained (generated protobuf plus gRPC only) so adapters
// developed in separate repositories do not depend on platform
// internals.
package registry

import (
	"context"
	"crypto/tls"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"
)

// CredentialRecord is the cacheable verification material returned with a
// successful authentication. It holds stored material, never a presented
// secret.
type CredentialRecord struct {
	// Type is the credential mechanism: TOKEN, PASSWORD, JWT or X509.
	Type string
	// CredentialsValue is the stored verification material.
	CredentialsValue string
	// Extra carries optional mechanism constraints.
	Extra []byte
	// CredentialEpoch is the tenant epoch stamped on the record.
	CredentialEpoch int64
	// ExpiresAt bounds the credential validity; meaningful only when
	// HasExpiry is true.
	ExpiresAt time.Time
	// HasExpiry reports whether the credential carries an expiry.
	HasExpiry bool
}

// DetailedResult is the full authentication outcome, including the record
// an ingress may cache.
type DetailedResult struct {
	// OK reports whether authentication succeeded.
	OK bool
	// Identity is the resolved device identity when OK.
	Identity sdktelemetry.DeviceIdentity
	// Record is the verification material when OK; nil otherwise.
	Record *CredentialRecord
}

// moduleTokenHeader carries the shared module/ingress token as gRPC
// metadata, matching the HTTP Authorization bearer convention.
const moduleTokenHeader = "authorization"

// Client is the registry gRPC client used by adapter services.
type Client struct {
	client v1.RegistryAuthClient
	conn   *grpc.ClientConn
	token  string
}

// NewClient dials the registry gRPC endpoint. Dev traffic may run over
// loopback without transport security; cluster deployments select mTLS
// credentials as defined in ADR-0007. When moduleToken is non-empty it
// is attached to every call as a bearer credential.
func NewClient(address, moduleToken string) (*Client, error) {
	return NewClientWithTLS(address, moduleToken, nil)
}

// NewClientWithTLS dials the registry gRPC endpoint with an explicit TLS
// configuration (ADR-0003 internal-link encryption). A nil tlsConfig keeps
// the legacy plaintext loopback behavior; callers pass a TLS 1.3 config
// (with a client certificate for mTLS) in cluster deployments.
func NewClientWithTLS(address, moduleToken string, tlsConfig *tls.Config) (*Client, error) {
	var transport credentials.TransportCredentials
	if tlsConfig != nil {
		transport = credentials.NewTLS(tlsConfig)
	} else {
		transport = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(transport))
	if err != nil {
		return nil, err
	}
	return &Client{
		client: v1.NewRegistryAuthClient(conn),
		conn:   conn,
		token:  moduleToken,
	}, nil
}

// withToken returns a context carrying the bearer token when one is set.
func (c *Client) withToken(ctx context.Context) context.Context {
	if c.token == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, moduleTokenHeader,
		"Bearer "+c.token)
}

// Close releases the underlying gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// Authenticate validates a device credential through the registry gRPC.
// It returns the resolved device identity when authentication succeeds.
func (c *Client) Authenticate(ctx context.Context, username, password string,
) (sdktelemetry.DeviceIdentity, bool, error) {
	res, err := c.AuthenticateDetailed(ctx, username, password)
	if err != nil {
		return sdktelemetry.DeviceIdentity{}, false, err
	}
	return res.Identity, res.OK, nil
}

// AuthenticateDetailed validates a device credential and returns the full
// outcome including the verification record for caching.
func (c *Client) AuthenticateDetailed(ctx context.Context, username, password string,
) (DetailedResult, error) {
	return c.callDetailed(c.withToken(ctx), &v1.AuthenticateRequest{
		CredentialsId:    username,
		CredentialsValue: password,
	})
}

// AuthenticateCertificate validates a leaf certificate presented at the
// mTLS layer against the X509 credential identified by credentialsID.
// The fingerprint is the lowercase hex SHA-256 of the certificate DER.
func (c *Client) AuthenticateCertificate(ctx context.Context, credentialsID, fingerprint string,
) (sdktelemetry.DeviceIdentity, bool, error) {
	res, err := c.AuthenticateCertificateDetailed(ctx, credentialsID, fingerprint)
	if err != nil {
		return sdktelemetry.DeviceIdentity{}, false, err
	}
	return res.Identity, res.OK, nil
}

// AuthenticateCertificateDetailed validates an mTLS leaf certificate and
// returns the full outcome including the verification record for caching.
func (c *Client) AuthenticateCertificateDetailed(ctx context.Context, credentialsID, fingerprint string,
) (DetailedResult, error) {
	return c.callDetailed(c.withToken(ctx), &v1.AuthenticateRequest{
		CredentialsId:          credentialsID,
		CertificateFingerprint: fingerprint,
	})
}

// callDetailed performs one authentication RPC and maps the response into a
// DetailedResult.
func (c *Client) callDetailed(ctx context.Context, req *v1.AuthenticateRequest) (DetailedResult, error) {
	resp, err := c.client.Authenticate(ctx, req)
	if err != nil {
		return DetailedResult{}, err
	}
	if !resp.GetOk() || resp.Device == nil {
		return DetailedResult{OK: false}, nil
	}
	result := DetailedResult{
		OK: true,
		Identity: sdktelemetry.DeviceIdentity{
			TenantID:     resp.Device.TenantId,
			DeviceID:     resp.Device.DeviceId,
			Capabilities: resp.Device.Capabilities,
		},
	}
	if r := resp.GetRecord(); r != nil {
		record := &CredentialRecord{
			Type:             r.Type,
			CredentialsValue: r.CredentialsValue,
			Extra:            r.Extra,
			CredentialEpoch:  r.CredentialEpoch,
		}
		if r.ExpiresAtUnix > 0 {
			record.ExpiresAt = time.Unix(r.ExpiresAtUnix, 0)
			record.HasExpiry = true
		}
		result.Record = record
	}
	return result, nil
}
