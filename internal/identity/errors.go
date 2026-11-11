// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// errors.go collects sentinel identity errors; REST handlers map them
// to standard status codes.

package identity

import "errors"

var (
	// ErrUserNotFound means no user matches the identifier in scope.
	ErrUserNotFound = errors.New("user not found")

	// ErrUserDisabled means the account was disabled by an admin.
	ErrUserDisabled = errors.New("user is disabled")

	// ErrUserLocked means too many failed logins and the lock window is
	// still active.
	ErrUserLocked = errors.New("user is temporarily locked")

	// ErrInvalidCredentials means the identifier/password pair is wrong.
	ErrInvalidCredentials = errors.New("invalid credentials")

	// ErrLoginAmbiguous means the email matches users of multiple
	// tenants, so a tenant hint is required to disambiguate. User
	// creation normally prevents this via global email uniqueness.
	ErrLoginAmbiguous = errors.New("email matches more than one tenant")

	// ErrSessionNotFound means the refresh token is unknown.
	ErrSessionNotFound = errors.New("refresh session not found")

	// ErrSessionExpired means the refresh token reached its TTL.
	ErrSessionExpired = errors.New("refresh session expired")

	// ErrSessionRevoked means the session was explicitly revoked.
	ErrSessionRevoked = errors.New("refresh session revoked")

	// ErrInvalidArgument means a malformed identity request.
	ErrInvalidArgument = errors.New("invalid identity argument")

	// ErrPasswordNotSet means the first-run administrator password has not
	// been configured yet. The caller should complete bootstrap rather than
	// retry the password.
	ErrPasswordNotSet = errors.New("administrator password not set")
)
