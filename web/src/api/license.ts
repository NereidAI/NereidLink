// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// license.ts is the platform-administration client for the deployment
// license (ADR-0024). It reads the current deployment license gate state
// and uploads a vendor-signed license envelope. Both endpoints are
// platform-super-admin scoped; a tenant-scope envelope is rejected by the
// server.

import client from "./client";

// DeploymentLicense mirrors license.DeploymentInfo on the server.
export interface DeploymentLicense {
  installed: boolean;
  customer_name?: string;
  tenant_cap: number;
  tenant_count: number;
  issued_at?: string;
  expires_at?: string;
  features: string[];
  read_only: boolean;
  reason?: string;
}

// LicenseEnvelope is the signed offline license file accepted by both the
// REST upload and the `nereidlink license install` CLI.
export interface LicenseEnvelope {
  payload: string;
  signature: string;
}

// getDeploymentLicense returns the installed deployment license and state.
export async function getDeploymentLicense(): Promise<DeploymentLicense> {
  const { data } = await client.get<DeploymentLicense>("/v1/platform/license");
  return data;
}

// installDeploymentLicense verifies and replaces the deployment license.
export async function installDeploymentLicense(
  envelope: LicenseEnvelope,
): Promise<DeploymentLicense> {
  const { data } = await client.put<DeploymentLicense>(
    "/v1/platform/license",
    envelope,
  );
  return data;
}
