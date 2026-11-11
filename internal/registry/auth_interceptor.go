// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// tokenInterceptor rejects calls missing the shared module token when the
// registry is configured with one. It is the authentication boundary for
// southbound modules once the gRPC endpoint is no longer loopback-only;
// mTLS replaces or augments it in hardened deployments (ADR-0007).
type tokenInterceptor struct {
	token string
}

// NewTokenInterceptor constructs the interceptor.
func NewTokenInterceptor(token string) *tokenInterceptor {
	return &tokenInterceptor{token: token}
}

// Unary validates the bearer token before invoking the handler.
func (i *tokenInterceptor) Unary(ctx context.Context, req any,
	_ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if i.token != "" && !i.authorized(ctx) {
		return nil, status.Error(codes.Unauthenticated, "missing or invalid module token")
	}
	return handler(ctx, req)
}

// authorized reports whether the metadata carries the expected bearer token.
func (i *tokenInterceptor) authorized(ctx context.Context) bool {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return false
	}
	got, found := strings.CutPrefix(values[0], "Bearer ")
	return found && got == i.token
}
