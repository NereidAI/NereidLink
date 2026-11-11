// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// helpers.go contains shared constants and small utilities for opinvoke.

package opinvoke

import "time"

// DispatchSubjectPrefix is the Core NATS dispatch root; the ingress id is
// appended: nl.edge.op.dispatch.<ingress>. The owning ingress answers with
// one ResultFrame per request.
const DispatchSubjectPrefix = "nl.edge.op.dispatch."

// DefaultHistoryLimit bounds one history query when the caller omits a
// value.
const DefaultHistoryLimit = 50

// Retention is the task retention window; completed rows older than this are
// deleted by the sweep. The permanent evidence is the chained audit log.
const Retention = 90 * 24 * time.Hour

// formatUTC renders a time as RFC3339 milliseconds in UTC.
func formatUTC(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}
