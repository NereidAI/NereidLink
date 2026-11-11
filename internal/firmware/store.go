// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists firmware package metadata. Binary bytes live in object
// storage; this layer records only the object key and integrity metadata and
// enforces tenant scoping and the (tenant, profile, version) uniqueness that
// powers upgrade/rollback selection.

package firmware

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/apipage"
)

// Store is the PostgreSQL-backed firmware repository.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a firmware store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// pgUniqueViolation is the PostgreSQL unique-violation SQLSTATE.
const pgUniqueViolation = "23505"

// nullableText maps an empty string to SQL NULL (used for release_notes).
func nullableText(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// packageColumns is the canonical read column list.
const packageColumns = `id::text, tenant_id::text,
	COALESCE(profile_id::text, ''), name, COALESCE(release_notes, ''),
	version, filename, content_type,
	object_key, size_bytes, sha256, is_stable,
	COALESCE(created_by::text, ''), created_at, updated_at`

// scanPackage maps one row using packageColumns.
func scanPackage(row pgx.Row) (Package, error) {
	var p Package
	err := row.Scan(&p.ID, &p.TenantID, &p.ProfileID, &p.Name, &p.ReleaseNotes,
		&p.Version, &p.Filename,
		&p.ContentType, &p.ObjectKey, &p.SizeBytes, &p.SHA256, &p.IsStable,
		&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// ProfileBelongsToTenant reports whether a device profile is owned by the
// tenant, used to reject cross-tenant compatibility bindings.
func (s *Store) ProfileBelongsToTenant(ctx context.Context, tenantID, profileID string) (bool, error) {
	if _, err := uuid.Parse(profileID); err != nil {
		return false, nil
	}
	var ok bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM device_profile WHERE id=$1 AND tenant_id=$2)`,
		profileID, tenantID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("check firmware profile ownership: %w", err)
	}
	return ok, nil
}

// Create inserts one package row. The caller has already uploaded the bytes
// and computed size/sha256.
func (s *Store) Create(ctx context.Context, p Package) (Package, error) {
	var createdBy any
	if p.CreatedBy != "" {
		if _, err := uuid.Parse(p.CreatedBy); err != nil {
			return Package{}, fmt.Errorf("%w: invalid creator", ErrInvalidArgument)
		}
		createdBy = p.CreatedBy
	}
	var profileID any
	if p.ProfileID != "" {
		if _, err := uuid.Parse(p.ProfileID); err != nil {
			return Package{}, fmt.Errorf("%w: invalid profile id", ErrInvalidArgument)
		}
		profileID = p.ProfileID
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Package{}, fmt.Errorf("insert firmware package: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Enforce stable exclusivity at write time too: an upload marked stable
	// clears the previous star in its (tenant, model) scope in the same tx.
	if p.IsStable {
		if profileID == nil {
			if _, err := tx.Exec(ctx,
				`UPDATE firmware_package SET is_stable=false, updated_at=now()
				 WHERE tenant_id=$1 AND is_stable=true AND profile_id IS NULL`,
				p.TenantID); err != nil {
				return Package{}, fmt.Errorf("clear previous stable: %w", err)
			}
		} else {
			if _, err := tx.Exec(ctx,
				`UPDATE firmware_package SET is_stable=false, updated_at=now()
				 WHERE tenant_id=$1 AND is_stable=true AND profile_id=$2`,
				p.TenantID, p.ProfileID); err != nil {
				return Package{}, fmt.Errorf("clear previous stable: %w", err)
			}
		}
	}
	row := tx.QueryRow(ctx,
		`INSERT INTO firmware_package
		   (id, tenant_id, profile_id, name, release_notes, version, filename,
		    content_type, object_key, size_bytes, sha256, is_stable, created_by)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		 RETURNING `+packageColumns,
		uuid.NewString(), p.TenantID, profileID, p.Name, nullableText(p.ReleaseNotes),
		p.Version, p.Filename,
		p.ContentType, p.ObjectKey, p.SizeBytes, p.SHA256, p.IsStable, createdBy)
	created, err := scanPackage(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return Package{}, ErrDuplicate
		}
		return Package{}, fmt.Errorf("insert firmware package: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Package{}, fmt.Errorf("commit firmware package: %w", err)
	}
	return created, nil
}

// Get returns one tenant-scoped package.
func (s *Store) Get(ctx context.Context, tenantID, id string) (Package, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Package{}, ErrNotFound
	}
	p, err := scanPackage(s.pool.QueryRow(ctx,
		`SELECT `+packageColumns+` FROM firmware_package
		 WHERE tenant_id=$1 AND id=$2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Package{}, ErrNotFound
	}
	if err != nil {
		return Package{}, fmt.Errorf("get firmware package: %w", err)
	}
	return p, nil
}

// Delete removes one tenant-scoped row and returns the deleted package so the
// caller can also remove its blob.
func (s *Store) Delete(ctx context.Context, tenantID, id string) (Package, error) {
	p, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return Package{}, err
	}
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM firmware_package WHERE tenant_id=$1 AND id=$2`,
		tenantID, id); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return Package{}, ErrInUse
		}
		return Package{}, fmt.Errorf("delete firmware package: %w", err)
	}
	return p, nil
}

// SetStable flips the stable marker of one package. Marking a package stable
// first clears the previous star in the same (tenant, model) scope so at most
// one recommended rollback target exists per model (ADR-0006d §2); the partial
// unique index is the database backstop for this transaction.
func (s *Store) SetStable(ctx context.Context, tenantID, id string, stable bool) (Package, error) {
	if _, err := uuid.Parse(id); err != nil {
		return Package{}, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Package{}, fmt.Errorf("set firmware stable: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var profileID *string
	if err := tx.QueryRow(ctx,
		`SELECT profile_id::text FROM firmware_package
		 WHERE tenant_id=$1 AND id=$2`, tenantID, id).Scan(&profileID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Package{}, ErrNotFound
		}
		return Package{}, fmt.Errorf("set firmware stable: %w", err)
	}
	if stable {
		if profileID == nil {
			if _, err := tx.Exec(ctx,
				`UPDATE firmware_package SET is_stable=false, updated_at=now()
				 WHERE tenant_id=$1 AND is_stable=true AND profile_id IS NULL
				   AND id <> $2`, tenantID, id); err != nil {
				return Package{}, fmt.Errorf("clear previous stable: %w", err)
			}
		} else {
			if _, err := tx.Exec(ctx,
				`UPDATE firmware_package SET is_stable=false, updated_at=now()
				 WHERE tenant_id=$1 AND is_stable=true AND profile_id=$2
				   AND id <> $3`, tenantID, *profileID, id); err != nil {
				return Package{}, fmt.Errorf("clear previous stable: %w", err)
			}
		}
	}
	p, err := scanPackage(tx.QueryRow(ctx,
		`UPDATE firmware_package SET is_stable=$3, updated_at=now()
		 WHERE tenant_id=$1 AND id=$2
		 RETURNING `+packageColumns, tenantID, id, stable))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return Package{}, ErrDuplicate
		}
		return Package{}, fmt.Errorf("set firmware stable: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Package{}, fmt.Errorf("commit stable: %w", err)
	}
	return p, nil
}

// List returns a filtered, newest-first page of packages.
func (s *Store) List(ctx context.Context, tenantID string, filter Filter) (apipage.Page[Package], error) {
	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize < 1 {
		pageSize = 20
	}

	// Build tenant + optional filters. Search covers name, version and
	// filename; ProfileID "" means all, "none" means profile-agnostic.
	conds := []string{"tenant_id = $1"}
	args := []any{tenantID}
	arg := 2
	if search := strings.TrimSpace(filter.Search); search != "" {
		conds = append(conds,
			fmt.Sprintf("(name ILIKE '%%' || $%d || '%%' OR version ILIKE '%%' || $%d || '%%' OR filename ILIKE '%%' || $%d || '%%')", arg, arg, arg))
		args = append(args, search)
		arg++
	}
	switch filter.ProfileID {
	case "none":
		conds = append(conds, "profile_id IS NULL")
	case "":
		// no filter
	default:
		if _, err := uuid.Parse(filter.ProfileID); err != nil {
			return apipage.Page[Package]{}, fmt.Errorf("%w: invalid profile id", ErrInvalidArgument)
		}
		conds = append(conds, fmt.Sprintf("profile_id = $%d", arg))
		args = append(args, filter.ProfileID)
		arg++
	}
	where := strings.Join(conds, " AND ")

	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM firmware_package WHERE `+where, args...).Scan(&total); err != nil {
		return apipage.Page[Package]{}, fmt.Errorf("count firmware packages: %w", err)
	}

	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.pool.Query(ctx,
		`SELECT `+packageColumns+` FROM firmware_package
		 WHERE `+where+` ORDER BY created_at DESC
		 LIMIT $`+strconv.Itoa(arg)+` OFFSET $`+strconv.Itoa(arg+1), args...)
	if err != nil {
		return apipage.Page[Package]{}, fmt.Errorf("list firmware packages: %w", err)
	}
	defer rows.Close()

	items := []Package{}
	for rows.Next() {
		p, err := scanPackage(rows)
		if err != nil {
			return apipage.Page[Package]{}, err
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return apipage.Page[Package]{}, err
	}
	return apipage.New(items, total, page, pageSize), nil
}
