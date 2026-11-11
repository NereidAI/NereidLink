<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Account Security

The **Account Security** page (under the account menu) protects your own login
identity. It manages two-factor authentication and lists active login
sessions.

## Two-factor authentication (TOTP)

- When TOTP is disabled, choose **Enable** and scan the QR code with an
  authenticator app (or copy the secret manually).
- Enter the six-digit code the app shows to finish enrollment. NereidLink then
  displays a set of **recovery codes**. Save them somewhere safe: each code
  lets you log in once if you lose the authenticator. Recovery codes are shown
  only once.
- While TOTP is enabled, every login requires both your password and a current
  code.
- Choose **Disable** to remove the second factor.

## User API keys

API keys are long-lived credentials for external integrations acting on your
behalf. Send one in the `X-Api-Key` header instead of an interactive bearer
token. A key uses the format `nlk_` followed by 52 lowercase unpadded base32
characters.

- Create a key with a descriptive name. Leave **Permission scopes** empty to
  inherit your current role permissions, or enter explicit permission points.
  An explicit scope must already be held by your account; if your role is later
  narrowed, the key can no longer use that permission.
- The full key is displayed exactly once after creation or rotation. The list
  only shows a non-secret prefix, state, scope, expiry and last-used time.
  Revoked or expired keys cannot be rotated; create a replacement instead.
- Revoke a key to disable it immediately while retaining its audit history.
  Hard delete removes the stored row; audit events keep the key id and prefix.
- API-key management requires an interactive login session. A key cannot create,
  rotate, revoke or delete other keys.
- See [Integrate with user API keys](help:page/integrate-api-keys) for request
  examples and permission behavior.

## Login sessions

The session list shows every session created with your credentials: client
user agent, source IP address, creation time, expiry time, and whether the
session is currently active.

- Your current session is marked and cannot be revoked from this page.
- Revoke any other session to sign that client out immediately. Revoking a
  session does not change your password; if you suspect account compromise,
  revoke the unfamiliar sessions and change your password as well.

Session management applies to your identity across every tenant you belong
to; an administrator cannot enable TOTP on your behalf.

## Single sign-on (SSO / OIDC)

When an external identity provider (such as Keycloak, Authing or Entra ID) is
configured by the operator, the login page shows a **Sign in with SSO** button
alongside the email/password form.

- Clicking it redirects you to your identity provider. After you approve, you
  return automatically and are signed in; you do not create a password in
  NereidLink.
- Your account is created on first login and assigned a role from the
  identity-provider group your administrator configured. If your groups are not
  mapped to a role, sign-in is refused; ask your administrator.
- Sign-out and session/recovery behavior on this page apply to SSO sessions
  too. MFA for SSO users is enforced by the identity provider, not here.

## Suite single sign-on (NereidSphere ticket)

When the deployment is joined to a NereidSphere (NS) suite portal, the normal
entry point is the portal: open the product there, and NS redirects you back
with a one-time ticket that is exchanged automatically. If you land on the
NereidLink login page directly, it shows a **Sign in with NereidSphere**
button that returns you to the portal.

- The ticket is single-use and expires in 30 seconds. Using the browser
  **Back** button or refreshing the callback page fails with an "expired or
  already used" message; start again from the portal.
- Your identity, roles and email verification come from the identity plane
  through NS; you still get an ordinary NereidLink session afterwards.
- The suite administrator assigns you **one role per tenant**. Those
  assignments take effect on every login and are reconciled automatically:
  if an assignment is removed in the suite console, your access to that
  tenant is removed on your next login. Locally managed customer-level
  access is not affected.
- If your email matches the platform administrator account, you sign in as
  that administrator; otherwise a tenant account is provisioned on first
  login.
