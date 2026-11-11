// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// witnesses.go implements the three independent clock-rollback witnesses
// required by ADR-0024: PostgreSQL, NATS KV and a 0600 local file.

package license

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	natjs "github.com/nats-io/nats.go/jetstream"

	"github.com/jackc/pgx/v5"
)

// observeClock advances every witness when now is later than its stored
// value. Existing older values are preserved, so a rollback cannot erase
// the highest trusted time.
func (s *Service) observeClock(ctx context.Context, now time.Time) error {
	if err := s.advancePGWitness(ctx, now); err != nil {
		return fmt.Errorf("postgres witness: %w", err)
	}
	if s.js != nil {
		if err := s.advanceNATSWitness(ctx, now); err != nil {
			return fmt.Errorf("nats witness: %w", err)
		}
	}
	if s.clockFile != "" {
		if err := s.advanceFileWitness(now); err != nil {
			return fmt.Errorf("file witness: %w", err)
		}
	}
	return nil
}

// clockRollback returns the amount by which current time appears behind
// the strongest witness. Zero means the clocks agree or no witness exists.
func (s *Service) clockRollback(ctx context.Context, now time.Time) (time.Duration, error) {
	var latest time.Time
	if value, err := s.pgWitness(ctx); err != nil {
		return 0, err
	} else if value.After(latest) {
		latest = value
	}
	if s.js != nil {
		if value, err := s.natsWitness(ctx); err != nil {
			return 0, err
		} else if value.After(latest) {
			latest = value
		}
	}
	if s.clockFile != "" {
		if value, ok, err := fileWitness(s.clockFile); err != nil {
			return 0, err
		} else if ok && value.After(latest) {
			latest = value
		}
	}
	if latest.After(now) {
		return latest.Sub(now), nil
	}
	return 0, nil
}

// clockRestriction reports the read-only reason for excessive rollback.
func (s *Service) clockRestriction(ctx context.Context, now time.Time) (string, bool) {
	rollback, err := s.clockRollback(ctx, now)
	if err != nil {
		slog.Error("failed to read license clock witnesses", "error", err)
		return "", false
	}
	if rollback > clockTolerance {
		return fmt.Sprintf("clock rollback %s exceeds tolerance", rollback), true
	}
	return "", false
}

// refreshSuspended reloads the immediate write-stop cache from PostgreSQL.
func (s *Service) refreshSuspended(ctx context.Context) error {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text FROM tenant WHERE status = 'SUSPENDED'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	values := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		values[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.suspendedMu.Lock()
	s.suspended = values
	s.suspendedMu.Unlock()
	return nil
}

func (s *Service) advancePGWitness(ctx context.Context, now time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO license_clock_witness (id, last_known_time)
		 VALUES (1, $1)
		 ON CONFLICT (id) DO UPDATE SET
		     last_known_time = GREATEST(
		         license_clock_witness.last_known_time, EXCLUDED.last_known_time),
		     updated_at = now()`, now)
	return err
}

func (s *Service) pgWitness(ctx context.Context) (time.Time, error) {
	var value time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT last_known_time FROM license_clock_witness WHERE id = 1`).
		Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	return value, err
}

func (s *Service) clockKV(ctx context.Context) (natjs.KeyValue, error) {
	kv, err := s.js.KeyValue(ctx, clockKVBucket)
	if errors.Is(err, natjs.ErrBucketNotFound) {
		return s.js.CreateKeyValue(ctx, natjs.KeyValueConfig{
			Bucket:   clockKVBucket,
			Replicas: s.jsReplicas,
		})
	}
	return kv, err
}

func (s *Service) advanceNATSWitness(ctx context.Context, now time.Time) error {
	kv, err := s.clockKV(ctx)
	if err != nil {
		return err
	}
	current, err := s.natsWitness(ctx)
	if err != nil {
		return err
	}
	if !now.After(current) {
		return nil
	}
	_, err = kv.PutString(ctx, clockKVKey, now.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Service) natsWitness(ctx context.Context) (time.Time, error) {
	kv, err := s.clockKV(ctx)
	if err != nil {
		return time.Time{}, err
	}
	entry, err := kv.Get(ctx, clockKVKey)
	if errors.Is(err, natjs.ErrKeyNotFound) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, string(entry.Value()))
	if err != nil {
		return time.Time{}, err
	}
	return parsed, nil
}

func (s *Service) advanceFileWitness(now time.Time) error {
	current, ok, err := fileWitness(s.clockFile)
	if err != nil {
		return err
	}
	if ok && !now.After(current) {
		return nil
	}
	data := []byte(now.UTC().Format(time.RFC3339Nano))
	// Write a sibling temp file and rename into place atomically: two
	// replicas share this path on an RWX volume, and a non-atomic write
	// can leave a zero-length file that the next read cannot parse.
	dir, name := filepath.Split(s.clockFile)
	tmp, err := os.CreateTemp(dir, ".clock-"+name+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, s.clockFile)
}

func fileWitness(path string) (time.Time, bool, error) {
	//nolint:gosec // operator-configured local clock witness path
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	// An empty/whitespace file is treated as "no witness yet": it can be
	// left behind by a crash or a torn write on a shared RWX volume and
	// must not disable clock-rollback detection with a parse error.
	if len(strings.TrimSpace(string(data))) == 0 {
		return time.Time{}, false, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, string(data))
	if err != nil {
		return time.Time{}, false, err
	}
	return parsed, true, nil
}
