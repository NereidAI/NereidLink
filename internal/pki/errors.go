// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// errors.go defines sentinel and error-mapping helpers for the trust
// manager. SQL integrity errors are mapped so the API layer can return
// stable codes instead of leaking PostgreSQL messages.

package pki

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrCANotFound means no trust anchor matched the given id.
var ErrCANotFound = errors.New("trust anchor not found")

// ErrInvalidCA marks malformed or non-CA certificate input.
var ErrInvalidCA = errors.New("invalid trust anchor")

// ErrDuplicateCA marks an import whose fingerprint already exists.
var ErrDuplicateCA = errors.New("duplicate trust anchor")

// mapCAError maps database errors raised by CA writes.
func mapCAError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			// Unique violation on fingerprint_sha256.
			return fmt.Errorf("%w: a trust anchor with this fingerprint already exists", ErrDuplicateCA)
		}
	}
	return err
}
