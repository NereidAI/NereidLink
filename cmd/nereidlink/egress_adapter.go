// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// egress_adapter.go adapts the rule-engine egress publisher to the concrete
// internal/egress publisher, assigning each forwarded envelope a unique id at
// production time.

package main

import (
	"context"

	"github.com/google/uuid"

	"github.com/nereidai/nereidlink/internal/egress"
	"github.com/nereidai/nereidlink/internal/rule"
)

// egressRuleAdapter implements rule.EgressPublisher.
type egressRuleAdapter struct {
	pub *egress.Publisher
}

// PublishForward converts and publishes one rule message.
func (a *egressRuleAdapter) PublishForward(
	ctx context.Context,
	forwarderID string,
	in rule.ForwardInput,
) error {
	return a.pub.Publish(ctx, forwarderID, egress.Envelope{
		ID:       uuid.NewString(),
		TenantID: in.TenantID,
		DeviceID: in.DeviceID,
		Ts:       in.Ts,
		TraceID:  in.TraceID,
		Channel:  in.Channel,
		Data:     in.Data,
		Metadata: in.Metadata,
	})
}
