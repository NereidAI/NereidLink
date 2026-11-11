// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// auth_handlers.go exposes the local identity endpoints: login, token
// refresh, logout and the current user. Login and refresh are public
// (rate limiting arrives with ADR-0014); every other REST route
// resolves the bearer token through the same AuthService.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/nereidai/nereidlink/internal/identity"
)

// AuthService is the authentication facade.
type AuthService interface {
	// Login verifies credentials and either issues a token pair or starts MFA.
	Login(ctx context.Context, email, password, ip, userAgent string) (identity.LoginResult, error)

	// BootstrapStatus reports whether first-run admin setup is still pending.
	BootstrapStatus(ctx context.Context) (identity.BootstrapStatus, error)

	// CompleteBootstrap sets the first admin password once and logs in.
	CompleteBootstrap(ctx context.Context, password, ip, userAgent string) (identity.TokenPair, error)

	// VerifyMFAChallenge completes second-factor verification.
	VerifyMFAChallenge(ctx context.Context, challengeToken, code, ip, userAgent string) (identity.TokenPair, error)

	// StartTOTP begins TOTP enrollment.
	StartTOTP(ctx context.Context, userID string) (identity.TOTPEnrollment, error)

	// ConfirmTOTP activates TOTP and returns recovery codes.
	ConfirmTOTP(ctx context.Context, userID, code string) (identity.RecoveryCodeSet, error)

	// DisableTOTP removes TOTP enrollment.
	DisableTOTP(ctx context.Context, userID string) error

	// MFAStatus returns MFA state.
	MFAStatus(ctx context.Context, userID string) (identity.MFAStatus, error)

	// Refresh rotates a refresh token.
	Refresh(ctx context.Context, refreshToken, ip, userAgent string) (identity.TokenPair, error)

	// Logout revokes the session of an access token.
	Logout(ctx context.Context, accessToken string) error

	// Current resolves the user of an access token.
	Current(ctx context.Context, accessToken string) (identity.User, error)

	// ListSessions lists the caller's login sessions.
	ListSessions(ctx context.Context, accessToken string) ([]identity.SessionInfo, error)

	// RevokeSession revokes one of the caller's sessions; current is
	// true when the revoked session backs the supplied access token.
	RevokeSession(ctx context.Context, accessToken, sessionID string) (current bool, err error)
}

// SetAuthService wires authentication.
func (h *Hub) SetAuthService(service AuthService) {
	h.authService = service
}

// loginBody is the credential request.
type loginBody struct {
	Email         string `json:"email"`
	Password      string `json:"password"`
	CaptchaID     string `json:"captcha_id"`
	CaptchaAnswer *int   `json:"captcha_answer"`
}

// refreshBody is the token rotation request.
type refreshBody struct {
	RefreshToken string `json:"refresh_token"`
}

// requireAuthService returns the facade or writes a 503.
func (h *Hub) requireAuthService(w http.ResponseWriter) AuthService {
	if h.authService == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "AUTH_SERVICE_UNAVAILABLE",
			"authentication is not enabled")
		return nil
	}
	return h.authService
}

// bootstrapStatusBody is the public first-run state returned to the SPA.
type bootstrapStatusBody struct {
	Required bool   `json:"required"`
	Email    string `json:"email,omitempty"`
}

// handleBootstrapStatus tells the SPA whether first-run setup is pending, so
// it can route to the setup screen instead of the login form. It is public.
func (h *Hub) handleBootstrapStatus(w http.ResponseWriter, r *http.Request) {
	service := h.requireAuthService(w)
	if service == nil {
		return
	}
	status, err := service.BootstrapStatus(r.Context())
	if h.writeAuthError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusOK, bootstrapStatusBody{
		Required: status.Required,
		Email:    status.Email,
	})
}

// bootstrapBody sets the first administrator password.
type bootstrapBody struct {
	Password string `json:"password"`
}

// handleBootstrap completes first-run administrator provisioning exactly once
// and returns a token pair, logging the new administrator straight in. It is
// unauthenticated but only succeeds while the admin password is still unset.
func (h *Hub) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	service := h.requireAuthService(w)
	if service == nil {
		return
	}
	var body bootstrapBody
	if !decodeBody(w, r, &body) {
		return
	}
	pair, err := service.CompleteBootstrap(r.Context(), body.Password,
		requestIP(r), r.UserAgent())
	if h.writeAuthError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusCreated, pair)
}

// handleLogin issues a token pair.
func (h *Hub) handleLogin(w http.ResponseWriter, r *http.Request) {
	service := h.requireAuthService(w)
	if service == nil {
		return
	}
	var body loginBody
	if !decodeBody(w, r, &body) {
		return
	}
	// Verify the arithmetic challenge before touching identity so that
	// missing, wrong, expired or replayed answers never reach the password
	// check. The challenge is single-use regardless of outcome.
	if h.captcha == nil ||
		body.CaptchaAnswer == nil ||
		!h.captcha.verify(body.CaptchaID, *body.CaptchaAnswer) {
		writeRESTError(w, http.StatusUnauthorized, "CAPTCHA_INVALID",
			"captcha is invalid or expired")
		return
	}
	result, err := service.Login(r.Context(), body.Email, body.Password,
		requestIP(r), r.UserAgent())
	if h.writeAuthError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// handleRefresh rotates a refresh token.
func (h *Hub) handleRefresh(w http.ResponseWriter, r *http.Request) {
	service := h.requireAuthService(w)
	if service == nil {
		return
	}
	var body refreshBody
	if !decodeBody(w, r, &body) {
		return
	}
	pair, err := service.Refresh(r.Context(), body.RefreshToken,
		requestIP(r), r.UserAgent())
	if h.writeAuthError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusOK, pair)
}

// handleLogout revokes the caller's session.
func (h *Hub) handleLogout(w http.ResponseWriter, r *http.Request) {
	service := h.requireAuthService(w)
	if service == nil {
		return
	}
	if err := service.Logout(r.Context(), bearerToken(r)); h.writeAuthError(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCurrent returns the authenticated user.
func (h *Hub) handleCurrent(w http.ResponseWriter, r *http.Request) {
	service := h.requireAuthService(w)
	if service == nil {
		return
	}
	user, err := service.Current(r.Context(), bearerToken(r))
	if h.writeAuthError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusOK, user)
}

// handleListSessions returns the caller's active and historical sessions.
func (h *Hub) handleListSessions(w http.ResponseWriter, r *http.Request) {
	service := h.requireAuthService(w)
	if service == nil {
		return
	}
	sessions, err := service.ListSessions(r.Context(), bearerToken(r))
	if h.writeAuthError(w, err) {
		return
	}
	if sessions == nil {
		// Arrays serialize as [] rather than null per the API contract.
		sessions = []identity.SessionInfo{}
	}
	writeRESTJSON(w, http.StatusOK, sessions)
}

// handleRevokeSession revokes one of the caller's own sessions.
func (h *Hub) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	service := h.requireAuthService(w)
	if service == nil {
		return
	}
	sessionID := r.PathValue("session")
	current, err := service.RevokeSession(r.Context(), bearerToken(r), sessionID)
	if errors.Is(err, identity.ErrSessionNotFound) {
		writeRESTError(w, http.StatusNotFound, "SESSION_NOT_FOUND",
			"session not found or owned by another user")
		return
	}
	if h.writeAuthError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]any{
		"id":      sessionID,
		"current": current,
	})
}

// mfaVerifyBody completes an MFA challenge.
type mfaVerifyBody struct {
	ChallengeToken string `json:"challenge_token"`
	Code           string `json:"code"`
}

// totpConfirmBody activates a pending TOTP enrollment.
type totpConfirmBody struct {
	Code string `json:"code"`
}

// handleMFAVerify exchanges a challenge and second factor for a token pair.
func (h *Hub) handleMFAVerify(w http.ResponseWriter, r *http.Request) {
	service := h.requireAuthService(w)
	if service == nil {
		return
	}
	var body mfaVerifyBody
	if !decodeBody(w, r, &body) {
		return
	}
	pair, err := service.VerifyMFAChallenge(r.Context(),
		body.ChallengeToken, body.Code, requestIP(r), r.UserAgent())
	if h.writeAuthError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusOK, pair)
}

// handleMFAStatus returns the caller's MFA state.
func (h *Hub) handleMFAStatus(w http.ResponseWriter, r *http.Request) {
	principal := contextPrincipal(r.Context())
	if principal == nil {
		writeRESTError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing authenticated user")
		return
	}
	status, err := h.authService.MFAStatus(r.Context(), principal.UserID)
	if h.writeAuthError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusOK, status)
}

// handleTOTPSetup begins TOTP enrollment.
func (h *Hub) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	principal := contextPrincipal(r.Context())
	if principal == nil {
		writeRESTError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing authenticated user")
		return
	}
	enrollment, err := h.authService.StartTOTP(r.Context(), principal.UserID)
	if h.writeAuthError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusOK, enrollment)
}

// handleTOTPConfirm verifies and activates a pending enrollment.
func (h *Hub) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	principal := contextPrincipal(r.Context())
	if principal == nil {
		writeRESTError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing authenticated user")
		return
	}
	var body totpConfirmBody
	if !decodeBody(w, r, &body) {
		return
	}
	recoveryCodes, err := h.authService.ConfirmTOTP(r.Context(), principal.UserID, body.Code)
	if h.writeAuthError(w, err) {
		return
	}
	writeRESTJSON(w, http.StatusCreated, recoveryCodes)
}

// handleTOTPDisable removes the caller's TOTP enrollment.
func (h *Hub) handleTOTPDisable(w http.ResponseWriter, r *http.Request) {
	principal := contextPrincipal(r.Context())
	if principal == nil {
		writeRESTError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing authenticated user")
		return
	}
	if err := h.authService.DisableTOTP(r.Context(), principal.UserID); h.writeAuthError(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeAuthError maps identity failures to REST errors and reports
// whether an error was written.
func (h *Hub) writeAuthError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, identity.ErrPasswordNotSet):
		writeRESTError(w, http.StatusConflict, "PASSWORD_NOT_SET",
			"administrator password has not been set; complete first-run setup")
	case errors.Is(err, identity.ErrBootstrapComplete):
		writeRESTError(w, http.StatusConflict, "BOOTSTRAP_COMPLETE",
			"first-run setup is already complete")
	case errors.Is(err, identity.ErrInvalidCredentials):
		writeRESTError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS",
			"email or password is incorrect")
	case errors.Is(err, identity.ErrUserDisabled):
		writeRESTError(w, http.StatusForbidden, "USER_DISABLED", err.Error())
	case errors.Is(err, identity.ErrUserLocked):
		writeRESTError(w, http.StatusTooManyRequests, "USER_LOCKED", err.Error())
	case errors.Is(err, identity.ErrInvalidToken):
		writeRESTError(w, http.StatusUnauthorized, "INVALID_TOKEN", err.Error())
	case errors.Is(err, identity.ErrSessionNotFound),
		errors.Is(err, identity.ErrSessionRevoked),
		errors.Is(err, identity.ErrSessionExpired):
		writeRESTError(w, http.StatusUnauthorized, "INVALID_REFRESH_TOKEN", err.Error())
	case errors.Is(err, identity.ErrInvalidArgument):
		writeRESTError(w, http.StatusBadRequest, "AUTH_INVALID_ARGUMENT", err.Error())
	case errors.Is(err, identity.ErrInvalidPassword):
		writeRESTError(w, http.StatusBadRequest, "PASSWORD_POLICY", err.Error())
	default:
		writeRESTError(w, http.StatusInternalServerError, "AUTH_OPERATION_FAILED", err.Error())
	}
	return true
}

// bearerToken extracts the token from an Authorization header.
func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if strings.HasPrefix(header, prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}

// requestIP returns the peer address, honoring an explicit reverse-proxy
// header only when operators enable trusted proxies; RemoteAddr is the
// safe default for session records.
func requestIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		return strings.TrimSpace(strings.SplitN(forwarded, ",", 2)[0])
	}
	return r.RemoteAddr
}
