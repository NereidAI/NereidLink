// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists per-tenant sealed secrets (table tenant_secret, migration
// 0047) and resolves a set of names to plaintext for one downlink frame.
// Plaintext is only ever returned by Resolve and only to the internal MQTT
// relay; list/get REST surfaces use Metadata, which carries no value.

package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Domain errors.
var (
	// ErrInvalidName means the secret name fails the identifier rule.
	ErrInvalidName = errors.New("invalid secret name")
	// ErrEmptyValue means a write carried no secret material.
	ErrEmptyValue = errors.New("secret value must not be empty")
	// ErrNotFound means no secret with that name exists in the tenant.
	ErrNotFound = errors.New("secret not found")
)

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// ValidateName enforces the same rule as the table CHECK.
func ValidateName(name string) error {
	if !validName.MatchString(name) {
		return ErrInvalidName
	}
	return nil
}

// Store is the PostgreSQL-backed tenant secret vault.
type Store struct {
	pool  *pgxpool.Pool
	codec *Codec
}

// NewStore constructs a secret store.
func NewStore(pool *pgxpool.Pool, codec *Codec) *Store {
	return &Store{pool: pool, codec: codec}
}

// Storage kind values.
const (
	// KindBlob is the legacy single-value secret.
	KindBlob = "blob"
	// KindDocument is the typed/sealed JSON document.
	KindDocument = "document"
)

// Metadata is the REST-safe view of a secret (never carries a value).
type Metadata struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	SecretType  string    `json:"secret_type"`
	TypeVersion string    `json:"type_version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// metadataColumns is the REST-safe projection.
const metadataColumns = `id::text, tenant_id::text, name,
	CASE WHEN document_cipher IS NOT NULL THEN 'document' ELSE 'blob' END,
	secret_type, type_version, created_at, updated_at`

// scanMetadata decodes one metadata row.
func scanMetadata(row pgx.Row) (Metadata, error) {
	var md Metadata
	err := row.Scan(&md.ID, &md.TenantID, &md.Name, &md.Kind,
		&md.SecretType, &md.TypeVersion, &md.CreatedAt, &md.UpdatedAt)
	if err != nil {
		return Metadata{}, err
	}
	return md, nil
}

// Put validates and seals one single-value secret, upserting by
// (tenant, name). Writing a blob over an existing document resets the row
// to the legacy shape.
func (s *Store) Put(ctx context.Context, tenantID, name, value string) (Metadata, error) {
	if err := ValidateName(name); err != nil {
		return Metadata{}, err
	}
	if strings.TrimSpace(value) == "" {
		return Metadata{}, ErrEmptyValue
	}
	sealed, err := s.codec.Seal([]byte(value))
	if err != nil {
		return Metadata{}, err
	}
	row := s.pool.QueryRow(ctx,
		`INSERT INTO tenant_secret (tenant_id, name, ciphertext)
		 VALUES ($1,$2,$3)
		 ON CONFLICT (tenant_id, name) DO UPDATE
		 SET ciphertext=EXCLUDED.ciphertext,
		     document_cipher=NULL, secret_type='', type_version='',
		     updated_at=now()
		 RETURNING `+metadataColumns,
		tenantID, name, sealed)
	md, err := scanMetadata(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Metadata{}, ErrNotFound
	}
	return mapScanErr(md, err)
}

// PutDocument seals one typed JSON document whole, upserting by
// (tenant, name). Empty secretType is valid for ad-hoc documents. The
// legacy ciphertext column is cleared.
func (s *Store) PutDocument(ctx context.Context, tenantID, name, secretType, typeVersion string, document map[string]any) (Metadata, error) {
	if err := ValidateName(name); err != nil {
		return Metadata{}, err
	}
	if len(document) == 0 {
		return Metadata{}, ErrEmptyValue
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return Metadata{}, fmt.Errorf("encode secret document: %w", err)
	}
	sealed, err := s.codec.Seal(raw)
	if err != nil {
		return Metadata{}, err
	}
	row := s.pool.QueryRow(ctx,
		`INSERT INTO tenant_secret
		 (tenant_id, name, ciphertext, secret_type, type_version, document_cipher)
		 VALUES ($1,$2,NULL,$3,$4,$5)
		 ON CONFLICT (tenant_id, name) DO UPDATE
		 SET ciphertext=NULL,
		     secret_type=EXCLUDED.secret_type,
		     type_version=EXCLUDED.type_version,
		     document_cipher=EXCLUDED.document_cipher,
		     updated_at=now()
		 RETURNING `+metadataColumns,
		tenantID, name, secretType, typeVersion, sealed)
	md, err := scanMetadata(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Metadata{}, ErrNotFound
	}
	return mapScanErr(md, err)
}

// Get returns metadata for one named secret.
func (s *Store) Get(ctx context.Context, tenantID, name string) (Metadata, error) {
	md, err := scanMetadata(s.pool.QueryRow(ctx,
		`SELECT `+metadataColumns+`
		 FROM tenant_secret WHERE tenant_id=$1 AND name=$2`,
		tenantID, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return Metadata{}, ErrNotFound
	}
	return mapScanErr(md, err)
}

// List returns metadata for all of a tenant's secrets, alphabetical.
func (s *Store) List(ctx context.Context, tenantID string) ([]Metadata, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+metadataColumns+`
		 FROM tenant_secret WHERE tenant_id=$1 ORDER BY name`,
		tenantID)
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	defer rows.Close()
	out := []Metadata{}
	for rows.Next() {
		md, scanErr := scanMetadata(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, md)
	}
	return out, rows.Err()
}

// Delete removes one secret. A missing name is not an error (idempotent).
func (s *Store) Delete(ctx context.Context, tenantID, name string) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM tenant_secret WHERE tenant_id=$1 AND name=$2`,
		tenantID, name); err != nil {
		return fmt.Errorf("delete secret: %w", err)
	}
	return nil
}

// Resolve returns the decrypted plaintext keyed by name for the names present
// in the tenant. Blob rows yield their single value; document rows yield
// their compact JSON document, so a whole-name secret:// reference keeps
// working. Names that do not exist are simply absent from the map.
func (s *Store) Resolve(ctx context.Context, tenantID string, names []string) (map[string]string, error) {
	values := make(map[string]string, len(names))
	if len(names) == 0 {
		return values, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT name, ciphertext, document_cipher FROM tenant_secret
		 WHERE tenant_id=$1 AND name = ANY($2)`,
		tenantID, names)
	if err != nil {
		return nil, fmt.Errorf("resolve secrets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var blobCipher, docCipher *string
		if err := rows.Scan(&name, &blobCipher, &docCipher); err != nil {
			return nil, err
		}
		if docCipher != nil {
			plain, err := s.codec.Open(*docCipher)
			if err != nil {
				return nil, fmt.Errorf("open document %s: %w", name, err)
			}
			values[name] = string(plain)
			continue
		}
		plain, err := s.codec.Open(*blobCipher)
		if err != nil {
			return nil, fmt.Errorf("open secret %s: %w", name, err)
		}
		values[name] = string(plain)
	}
	return values, rows.Err()
}

// ResolveDocuments opens document rows keyed by name. Blob rows are absent
// from the map. The delivery path (ADR-0034) uses it to build typed frames.
func (s *Store) ResolveDocuments(ctx context.Context, tenantID string, names []string) (map[string]map[string]any, error) {
	values := make(map[string]map[string]any, len(names))
	if len(names) == 0 {
		return values, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT name, document_cipher FROM tenant_secret
		 WHERE tenant_id=$1 AND name = ANY($2)
		   AND document_cipher IS NOT NULL`,
		tenantID, names)
	if err != nil {
		return nil, fmt.Errorf("resolve secret documents: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, sealed string
		if err := rows.Scan(&name, &sealed); err != nil {
			return nil, err
		}
		plain, err := s.codec.Open(sealed)
		if err != nil {
			return nil, fmt.Errorf("open document %s: %w", name, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(plain, &doc); err != nil {
			return nil, fmt.Errorf("decode document %s: %w", name, err)
		}
		values[name] = doc
	}
	return values, rows.Err()
}

// ResolveConnectorSecrets is the narrow resolver the MQTT connector relay
// uses; it is Resolve under an ingress-facing name.
func (s *Store) ResolveConnectorSecrets(ctx context.Context, tenantID string, names []string) (map[string]string, error) {
	return s.Resolve(ctx, tenantID, names)
}

// mapScanErr converts a metadata scan error into vault domain errors, in
// particular the name CHECK violation surfaced by the database.
func mapScanErr(md Metadata, err error) (Metadata, error) {
	if err == nil {
		return md, nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" {
		return Metadata{}, ErrInvalidName
	}
	return Metadata{}, fmt.Errorf("scan secret: %w", err)
}
