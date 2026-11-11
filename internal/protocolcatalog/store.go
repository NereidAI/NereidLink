// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists the global protocol/device-service catalog in the
// protocol_catalog table (migration 0063). Rows are global vendor facts, so
// none of the queries take a tenant id; tenant scoping lives only in the REST
// authorization layer.

package protocolcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the PostgreSQL-backed catalog.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a catalog store.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const familyColumns = `family, display_name, COALESCE(service_name,''),
       COALESCE(protocol_key,''), transports, connection_fields, point_fields,
       default_point_address, COALESCE(doc_url,''), builtin, created_at, updated_at`

// List returns every catalog row ordered by builtin first then family. The
// console renders the whole table (it is a small fixed-size reference set);
// q optionally filters by a case-insensitive substring across the
// family/display/service/key columns.
func (s *Store) List(ctx context.Context, q string) ([]Family, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+familyColumns+`
		 FROM protocol_catalog
		 WHERE ($1 = '' OR family ILIKE $1 OR display_name ILIKE $1
		        OR service_name ILIKE $1 OR protocol_key ILIKE $1)
		 ORDER BY builtin DESC, family`, "%"+q+"%")
	if err != nil {
		return nil, fmt.Errorf("list protocol catalog: %w", err)
	}
	defer rows.Close()
	return scanFamilies(rows)
}

// Get returns one family by key.
func (s *Store) Get(ctx context.Context, family string) (Family, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+familyColumns+` FROM protocol_catalog WHERE family=$1`, family)
	return scanFamily(row)
}

// Create inserts a custom family. A duplicate key surfaces as ErrConflict.
func (s *Store) Create(ctx context.Context, in SaveInput) (Family, error) {
	transports, conn, point, addr, err := encodeInput(in)
	if err != nil {
		return Family{}, err
	}
	row := s.pool.QueryRow(ctx,
		`INSERT INTO protocol_catalog
		    (family, display_name, service_name, protocol_key, transports,
		     connection_fields, point_fields, default_point_address, doc_url,
		     builtin)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,FALSE)
		 RETURNING `+familyColumns,
		in.Family, in.DisplayName, in.ServiceName, in.ProtocolKey,
		transports, conn, point, addr, in.DocURL)
	fam, err := scanFamily(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Family{}, ErrConflict
		}
		return Family{}, err
	}
	return fam, nil
}

// Update replaces the mutable columns of one family. The family key and the
// builtin flag are immutable, so they never appear in the SET list.
func (s *Store) Update(ctx context.Context, family string, in SaveInput) (Family, error) {
	transports, conn, point, addr, err := encodeInput(in)
	if err != nil {
		return Family{}, err
	}
	row := s.pool.QueryRow(ctx,
		`UPDATE protocol_catalog SET
		    display_name=$2, service_name=$3, protocol_key=$4, transports=$5,
		    connection_fields=$6, point_fields=$7, default_point_address=$8,
		    doc_url=$9, updated_at=now()
		 WHERE family=$1
		 RETURNING `+familyColumns,
		family, in.DisplayName, in.ServiceName, in.ProtocolKey,
		transports, conn, point, addr, in.DocURL)
	return scanFamily(row)
}

// Delete removes a family. A builtin row is never deleted: the service checks
// the flag first; the SQL additionally keeps it regardless so a direct caller
// cannot bypass the rule.
func (s *Store) Delete(ctx context.Context, family string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM protocol_catalog WHERE family=$1 AND builtin=FALSE`, family)
	if err != nil {
		return fmt.Errorf("delete protocol family: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Distinguish missing from protected for an accurate status code.
		if _, getErr := s.Get(ctx, family); getErr == nil {
			return ErrBuiltin
		}
		return ErrNotFound
	}
	return nil
}

// encodeInput marshals the JSONB columns, coercing nil slices/objects to the
// non-null empty forms so the API never emits null arrays (API arrays are []).
func encodeInput(in SaveInput) (transports, conn, point, addr []byte, err error) {
	if transports, err = marshalOr(in.Transports, []Transport{}); err != nil {
		return
	}
	if conn, err = marshalOr(in.ConnectionFields, []FieldSpec{}); err != nil {
		return
	}
	if point, err = marshalOr(in.PointFields, []FieldSpec{}); err != nil {
		return
	}
	if len(in.DefaultPointAddress) == 0 || string(in.DefaultPointAddress) == "null" {
		addr = []byte("{}")
	} else {
		addr = in.DefaultPointAddress
	}
	return
}

// marshalOr marshals v, falling back to def when v is nil.
func marshalOr[T any](v []T, def []T) ([]byte, error) {
	if v == nil {
		v = def
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode catalog json: %w", err)
	}
	return b, nil
}

// rowScanner accepts pgx.Row or pgx.Rows.
type rowScanner interface{ Scan(dest ...any) error }

func scanFamilies(rows pgx.Rows) ([]Family, error) {
	out := []Family{}
	for rows.Next() {
		fam, err := scanFamily(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, fam)
	}
	return out, rows.Err()
}

func scanFamily(row rowScanner) (Family, error) {
	var fam Family
	var transports, conn, point, addr []byte
	err := row.Scan(&fam.Family, &fam.DisplayName, &fam.ServiceName,
		&fam.ProtocolKey, &transports, &conn, &point, &addr,
		&fam.DocURL, &fam.Builtin, &fam.CreatedAt, &fam.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Family{}, ErrNotFound
	}
	if err != nil {
		return Family{}, fmt.Errorf("scan protocol family: %w", err)
	}
	if err := json.Unmarshal(transports, &fam.Transports); err != nil {
		return Family{}, fmt.Errorf("decode transports: %w", err)
	}
	if err := json.Unmarshal(conn, &fam.ConnectionFields); err != nil {
		return Family{}, fmt.Errorf("decode connection_fields: %w", err)
	}
	if err := json.Unmarshal(point, &fam.PointFields); err != nil {
		return Family{}, fmt.Errorf("decode point_fields: %w", err)
	}
	fam.DefaultPointAddress = addr
	if fam.Transports == nil {
		fam.Transports = []Transport{}
	}
	if fam.ConnectionFields == nil {
		fam.ConnectionFields = []FieldSpec{}
	}
	if fam.PointFields == nil {
		fam.PointFields = []FieldSpec{}
	}
	return fam, nil
}
