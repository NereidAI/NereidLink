// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// stepUp.ts exposes the operation-time TOTP step-up flow (ADR-0035):
// start a challenge for a registered action, complete it with a current
// authenticator code, and hand the challenge id to the protected endpoint.

import client from "./client";
import { currentTenantId } from "./tenant";

// StepUpChallenge is one persisted challenge with its signed QR payload.
export interface StepUpChallenge {
  id: string;
  tenant_id: string;
  user_id: string;
  action: string;
  details_sha256: string;
  status: string;
  expires_at: string;
  signed_payload?: string;
}

// startStepUp creates a pending challenge for action with non-sensitive
// details (shown in the signed QR payload for verification).
export async function startStepUp(
  action: string,
  details?: Record<string, unknown>,
): Promise<StepUpChallenge> {
  const { data } = await client.post<StepUpChallenge>(
    `/v1/tenants/${currentTenantId()}/step-up/challenges`,
    { action, details },
  );
  return data;
}

// completeStepUp verifies a current TOTP code and returns the verified
// challenge.
export async function completeStepUp(
  challengeId: string,
  code: string,
): Promise<StepUpChallenge> {
  const { data } = await client.post<StepUpChallenge>(
    `/v1/tenants/${currentTenantId()}/step-up/challenges/${challengeId}`,
    { code },
  );
  return data;
}
