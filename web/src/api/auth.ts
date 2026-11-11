// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// auth.ts is the typed client for the local identity endpoints
// (ADR-0021): login, refresh rotation, logout and current user. Token
// storage is handled by the auth store, not by these calls.

import client from "./client";

// AuthUser is the authenticated user profile.
export interface AuthUser {
  id: string;
  tenant_id?: string;
  email: string;
  display_name: string;
  is_platform_admin: boolean;
  email_verified: boolean;
  must_change_password: boolean;
  last_login_at?: string;
  created_at: string;
  // Roles per membership tenant, keyed by tenant id. Populated on /me
  // and login responses; absent for accounts with no membership. Lets
  // the UI show tenant-admin surfaces (member administration) without
  // an extra round trip.
  tenant_roles?: Record<string, string[]>;
}

// TokenPair is a login/refresh response.
export interface TokenPair {
  access_token: string;
  refresh_token: string;
  token_type: string;
  expires_in: number;
  user: AuthUser;
}

// MFAChallenge is returned when a second factor is required.
export interface MFAChallenge {
  challenge_token: string;
  expires_in: number;
}

// LoginResult is either a token pair or an MFA challenge.
export interface LoginResult {
  mfa?: MFAChallenge;
  pair?: TokenPair;
}

// TOTPEnrollment starts authenticator setup.
export interface TOTPEnrollment {
  secret: string;
  uri: string;
}

// RecoveryCodes is shown once after TOTP confirmation.
export interface RecoveryCodes {
  codes: string[];
}

// MFAStatus reports MFA enrollment.
export interface MFAStatus {
  totp_enabled: boolean;
}

// CaptchaChallenge is a server-issued arithmetic login challenge.
export interface CaptchaChallenge {
  captcha_id: string;
  expression: string;
}

// BootstrapStatus reports whether first-run admin setup is still required.
export interface BootstrapStatus {
  required: boolean;
  email?: string;
}

// getBootstrapStatus reports whether the first administrator password has
// been set. It is a public endpoint so the pre-login SPA can route to the
// setup screen.
export async function getBootstrapStatus(): Promise<BootstrapStatus> {
  const { data } = await client.get<BootstrapStatus>(
    "/v1/auth/bootstrap-status",
  );
  return data;
}

// completeBootstrap sets the first administrator password exactly once and
// returns a token pair so the new admin is logged straight into the console.
export async function completeBootstrap(password: string): Promise<TokenPair> {
  const { data } = await client.post<TokenPair>("/v1/auth/bootstrap", {
    password,
  });
  return data;
}

// getCaptcha fetches a fresh one-time arithmetic challenge.
export async function getCaptcha(): Promise<CaptchaChallenge> {
  const { data } = await client.get<CaptchaChallenge>("/v1/auth/captcha");
  return data;
}

// login verifies credentials.
export async function login(
  email: string,
  password: string,
  captchaId: string,
  captchaAnswer: number,
): Promise<LoginResult> {
  const { data } = await client.post<LoginResult>("/v1/auth/login", {
    email,
    password,
    captcha_id: captchaId,
    captcha_answer: captchaAnswer,
  });
  return data;
}

// OIDCStatus reports whether external SSO is configured.
export interface OIDCStatus {
  enabled: boolean;
}

// getOIDCStatus queries the SSO availability flag.
export async function getOIDCStatus(): Promise<OIDCStatus> {
  const { data } = await client.get<OIDCStatus>("/v1/auth/oidc/status");
  return data;
}

// SSOStatus reports whether NereidSphere ticket SSO is configured and
// where the browser should go to start the suite SSO flow.
export interface SSOStatus {
  enabled: boolean;
  login_url?: string;
}

// getSSOStatus queries the NereidSphere ticket SSO availability flag.
export async function getSSOStatus(): Promise<SSOStatus> {
  const { data } = await client.get<SSOStatus>("/v1/auth/sso/status");
  return data;
}

// startOIDCLogin navigates the browser to the IdP authorization endpoint;
// the SPA does the full-page redirect itself (not an XHR).
export function startOIDCLogin(redirectTo: string): void {
  const params = new URLSearchParams();
  if (redirectTo) {
    params.set("redirect_to", redirectTo);
  }
  const qs = params.toString();
  window.location.assign(`/v1/auth/oidc/login${qs ? `?${qs}` : ""}`);
}

// verifyMFA completes an MFA challenge.
export async function verifyMFA(
  challengeToken: string,
  code: string,
): Promise<TokenPair> {
  const { data } = await client.post<TokenPair>("/v1/auth/mfa/verify", {
    challenge_token: challengeToken,
    code,
  });
  return data;
}

// mfaStatus returns the current MFA status.
export async function mfaStatus(): Promise<MFAStatus> {
  const { data } = await client.get<MFAStatus>("/v1/auth/mfa/status");
  return data;
}

// startTOTP begins TOTP setup.
export async function startTOTP(): Promise<TOTPEnrollment> {
  const { data } = await client.post<TOTPEnrollment>(
    "/v1/auth/mfa/totp/setup",
  );
  return data;
}

// confirmTOTP activates TOTP and returns recovery codes.
export async function confirmTOTP(code: string): Promise<RecoveryCodes> {
  const { data } = await client.post<RecoveryCodes>(
    "/v1/auth/mfa/totp/confirm",
    { code },
  );
  return data;
}

// disableTOTP removes TOTP enrollment.
export async function disableTOTP(): Promise<void> {
  await client.delete("/v1/auth/mfa/totp");
}

// refresh rotates a refresh token.
export async function refresh(refreshToken: string): Promise<TokenPair> {
  const { data } = await client.post<TokenPair>("/v1/auth/refresh", {
    refresh_token: refreshToken,
  });
  return data;
}

// me returns the current user.
export async function me(): Promise<AuthUser> {
  const { data } = await client.get<AuthUser>("/v1/auth/me");
  return data;
}

// logout revokes the current session.
export async function logout(): Promise<void> {
  await client.post("/v1/auth/logout");
}

// SessionInfo is a refresh session as returned by listSessions.
export interface SessionInfo {
  id: string;
  ip: string;
  user_agent: string;
  current: boolean;
  active: boolean;
  created_at: string;
  expires_at: string;
}

// listSessions returns the caller's active sessions.
export async function listSessions(): Promise<SessionInfo[]> {
  const { data } = await client.get<SessionInfo[]>("/v1/auth/sessions");
  return data;
}

// revokeSession revokes one of the caller's sessions.
export async function revokeSession(sessionId: string): Promise<{ id: string; current: boolean }> {
  const { data } = await client.delete<{ id: string; current: boolean }>(
    `/v1/auth/sessions/${sessionId}`
  );
  return data;
}
