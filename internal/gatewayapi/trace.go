// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// trace.go implements the request-correlation middleware (ADR-0017).
// Every REST request receives a trace ID — carried from an inbound
// X-Request-ID header when the caller supplies one, otherwise generated —
// that is echoed in the error envelope and available to handlers. This
// lets operators correlate a client-visible error with server logs without
// sharing internal identifiers.

package gatewayapi

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
)

// newTraceID generates a random 128-bit hex trace ID.
func newTraceID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(raw[:])
}

// traceWriter forwards writes to the wrapped ResponseWriter while carrying
// the trace ID for error envelopes. The Flusher/Hijacker passthrough keeps
// streaming and WebSocket upgrade paths working through the wrapper
// (mirrors L-004).
type traceWriter struct {
	http.ResponseWriter
	traceID string
}

// responseTraceID extracts the trace ID from a possibly-wrapped response
// writer by walking the middleware chain through Unwrap. An empty result
// means the trace middleware was not active.
func responseTraceID(w http.ResponseWriter) string {
	for {
		if tw, ok := w.(*traceWriter); ok {
			return tw.traceID
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return ""
		}
		w = unwrapper.Unwrap()
	}
}

// Flush forwards to the underlying flusher when available.
func (w *traceWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack forwards the underlying connection for protocol upgrades.
func (w *traceWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := w.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// traceMiddleware assigns a trace ID to every request and wraps the
// response writer so error envelopes can echo it.
func traceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get("X-Request-ID")
		if traceID == "" {
			traceID = newTraceID()
		}
		next.ServeHTTP(&traceWriter{ResponseWriter: w, traceID: traceID}, r)
	})
}
