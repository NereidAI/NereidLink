// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists profile delivery targets and per-gateway delivery state
// (migration 0085) and reads the library projection the renderers consume.

package profilepush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const deliveryColumns = `d.tenant_id::text, d.profile_id::text, p.name,
	d.gateway_id::text, d.desired_revision, d.dispatched_revision,
	d.applied_revision, d.state, d.reason, d.desired_at,
	d.dispatched_at, d.applied_at, d.failed_at, d.updated_at,
	EXISTS(SELECT 1 FROM profile_gateway_target t
	       WHERE t.tenant_id=d.tenant_id AND t.profile_id=d.profile_id
	         AND t.gateway_id=d.gateway_id)`

// LibraryProfile is the edge-visible content of one library profile, read from
// device_profile + measurement_descriptor. Description/labels are recovered
// from the stored edge_payload when present.
type LibraryProfile struct {
	// ID is the library profile id.
	ID string
	// Name is the tenant-unique name.
	Name string
	// Protocol is the open protocol family ("" unknown).
	Protocol string
	// Description is recovered from the stored payload when available.
	Description string
	// Labels are recovered from the stored payload when available.
	Labels []string
	// Points is the ordered active point set.
	Points []Point
	// Commands is recovered from the stored payload when available.
	Commands []Command
	// Revision is the current content revision.
	Revision int64
	// DesiredHash is the stored projection hash ("" before first render).
	DesiredHash string
}

// rawPoint is a neutral point as stored in edge_payload.
type rawPoint struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Unit        string          `json:"unit"`
	ReadWrite   string          `json:"read_write"`
	Min         *float64        `json:"min"`
	Max         *float64        `json:"max"`
	Default     string          `json:"default"`
	Description string          `json:"description"`
	Address     json.RawMessage `json:"address"`
}

// rawCommand is a neutral command as stored in edge_payload.
type rawCommand struct {
	Name   string   `json:"name"`
	Points []string `json:"points"`
}

// rawDocument is the neutral document subset read from edge_payload.
type rawDocument struct {
	Protocol    string       `json:"protocol"`
	Description string       `json:"description"`
	Labels      []string     `json:"labels"`
	Commands    []rawCommand `json:"commands"`
	Points      []rawPoint   `json:"points"`
}

// Store is the PostgreSQL profile-delivery store.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a profile-delivery store.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// LibraryProfile loads the edge-visible content of one library profile by id.
func (s *Store) LibraryProfile(ctx context.Context, tenantID, profileID string) (LibraryProfile, error) {
	return s.libraryProfile(ctx, `p.id=$2`, tenantID, profileID)
}

// LibraryProfileByName loads one library profile by tenant-unique name.
func (s *Store) LibraryProfileByName(ctx context.Context, tenantID, name string) (LibraryProfile, error) {
	return s.libraryProfile(ctx, `p.name=$2`, tenantID, name)
}

// libraryProfile reads one profile plus its active descriptors.
func (s *Store) libraryProfile(ctx context.Context, predicate, tenantID, key string) (LibraryProfile, error) {
	var (
		out        LibraryProfile
		rawPayload []byte
	)
	err := s.pool.QueryRow(ctx,
		`SELECT p.id::text, p.name, p.protocol, p.revision, p.desired_hash,
		        coalesce(p.edge_payload, '{}'::jsonb)
		 FROM device_profile p
		 WHERE p.tenant_id=$1 AND `+predicate,
		tenantID, key).Scan(&out.ID, &out.Name, &out.Protocol, &out.Revision,
		&out.DesiredHash, &rawPayload)
	if errors.Is(err, pgx.ErrNoRows) {
		return LibraryProfile{}, ErrNotFound
	}
	if err != nil {
		return LibraryProfile{}, fmt.Errorf("load library profile: %w", err)
	}

	var doc rawDocument
	if len(rawPayload) > 0 {
		_ = json.Unmarshal(rawPayload, &doc)
	}
	out.Description = doc.Description
	out.Labels = doc.Labels
	out.Commands = commandsFromRaw(doc.Commands)

	rows, err := s.pool.Query(ctx,
		`SELECT measurement_key, data_type, unit, read_write, address,
		        edge_default, edge_min, edge_max
		 FROM measurement_descriptor
		 WHERE tenant_id=$1 AND profile_id=$2 AND inactive=FALSE
		 ORDER BY measurement_key`,
		tenantID, out.ID)
	if err != nil {
		return LibraryProfile{}, fmt.Errorf("list profile points: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var pt Point
		var address []byte
		if err := rows.Scan(&pt.Name, &pt.Type, &pt.Unit, &pt.ReadWrite,
			&address, &pt.Default, &pt.Min, &pt.Max); err != nil {
			return LibraryProfile{}, err
		}
		pt.Address = json.RawMessage(address)
		// Recover the description from the payload when the descriptor
		// table does not carry one (it stores engineering, not prose).
		pt.Description = descriptionFor(doc.Points, pt.Name)
		out.Points = append(out.Points, pt)
	}
	if err := rows.Err(); err != nil {
		return LibraryProfile{}, err
	}
	return out, nil
}

// commandsFromRaw converts stored payload commands, or returns nil.
func commandsFromRaw(raw []rawCommand) []Command {
	if len(raw) == 0 {
		return nil
	}
	out := make([]Command, 0, len(raw))
	for _, cmd := range raw {
		out = append(out, Command(cmd))
	}
	return out
}

// descriptionFor finds the stored description for a point name.
func descriptionFor(points []rawPoint, name string) string {
	for _, pt := range points {
		if pt.Name == name {
			return pt.Description
		}
	}
	return ""
}

// AddTarget records an explicit distribution edge (profile -> gateway),
// idempotently.
func (s *Store) AddTarget(ctx context.Context, tenantID, profileID, gatewayID string, at time.Time) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO profile_gateway_target (tenant_id, profile_id, gateway_id, created_at)
		 VALUES ($1,$2,$3,$4)
		 ON CONFLICT (profile_id, gateway_id) DO NOTHING`,
		tenantID, profileID, gatewayID, at); err != nil {
		return fmt.Errorf("add profile target: %w", err)
	}
	return nil
}

// RemoveTarget drops an explicit distribution edge.
func (s *Store) RemoveTarget(ctx context.Context, tenantID, profileID, gatewayID string) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM profile_gateway_target
		 WHERE tenant_id=$1 AND profile_id=$2 AND gateway_id=$3`,
		tenantID, profileID, gatewayID); err != nil {
		return fmt.Errorf("remove profile target: %w", err)
	}
	return nil
}

// ListTargets lists the explicit distribution edges for one profile with the
// gateway display name joined from device.
func (s *Store) ListTargets(ctx context.Context, tenantID, profileID string) ([]Target, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT t.profile_id::text, t.gateway_id::text,
		        coalesce(d.canonical_name, ''), t.created_at
		 FROM profile_gateway_target t
		 LEFT JOIN device d ON d.id = t.gateway_id
		 WHERE t.tenant_id=$1 AND t.profile_id=$2
		 ORDER BY d.canonical_name, t.gateway_id::text`,
		tenantID, profileID)
	if err != nil {
		return nil, fmt.Errorf("list profile targets: %w", err)
	}
	defer rows.Close()
	out := []Target{}
	for rows.Next() {
		var t Target
		if err := rows.Scan(&t.ProfileID, &t.GatewayID, &t.GatewayName, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ReferencingGateways returns the distinct gateways whose connector instances
// reference one profile (the implicit distribution set).
func (s *Store) ReferencingGateways(ctx context.Context, tenantID, profileID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT gateway_id::text FROM connector_instance
		 WHERE tenant_id=$1 AND profile_id=$2`,
		tenantID, profileID)
	if err != nil {
		return nil, fmt.Errorf("list referencing gateways: %w", err)
	}
	defer rows.Close()
	return scanStrings(rows)
}

// ReferencedProfiles returns the distinct profiles a gateway's connector
// instances reference (the implicit distribution set for one gateway).
func (s *Store) ReferencedProfiles(ctx context.Context, tenantID, gatewayID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT profile_id::text FROM connector_instance
		 WHERE tenant_id=$1 AND gateway_id=$2 AND profile_id IS NOT NULL`,
		tenantID, gatewayID)
	if err != nil {
		return nil, fmt.Errorf("list referenced profiles: %w", err)
	}
	defer rows.Close()
	return scanStrings(rows)
}

// TargetedProfiles returns the distinct profiles explicitly targeted at one
// gateway.
func (s *Store) TargetedProfiles(ctx context.Context, tenantID, gatewayID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT profile_id::text FROM profile_gateway_target
		 WHERE tenant_id=$1 AND gateway_id=$2`,
		tenantID, gatewayID)
	if err != nil {
		return nil, fmt.Errorf("list targeted profiles: %w", err)
	}
	defer rows.Close()
	return scanStrings(rows)
}

// SyncDesired upserts delivery rows for one profile across its target gateways
// and bumps desired_revision to the profile's current revision. Rows whose
// revision changed reset to PENDING (re-delivery); unchanged rows are left as
// they are so an already-APPLIED delivery is not needlessly re-sent.
func (s *Store) SyncDesired(ctx context.Context, tenantID, profileID string, revision int64, gatewayIDs []string, at time.Time) error {
	for _, gatewayID := range gatewayIDs {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO profile_delivery
			 (tenant_id, profile_id, gateway_id, desired_revision, desired_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$5)
			 ON CONFLICT (profile_id, gateway_id) DO UPDATE
			 SET desired_revision=EXCLUDED.desired_revision,
			     state=CASE WHEN profile_delivery.desired_revision
			                     IS DISTINCT FROM EXCLUDED.desired_revision
			                THEN 'PENDING' ELSE profile_delivery.state END,
			     reason=CASE WHEN profile_delivery.desired_revision
			                      IS DISTINCT FROM EXCLUDED.desired_revision
			                 THEN '' ELSE profile_delivery.reason END,
			     pending_alarm=CASE WHEN profile_delivery.desired_revision
			                             IS DISTINCT FROM EXCLUDED.desired_revision
			                        THEN FALSE ELSE profile_delivery.pending_alarm END,
			     failed_alarm=CASE WHEN profile_delivery.desired_revision
			                            IS DISTINCT FROM EXCLUDED.desired_revision
			                       THEN FALSE ELSE profile_delivery.failed_alarm END,
			     failed_critical=CASE WHEN profile_delivery.desired_revision
			                               IS DISTINCT FROM EXCLUDED.desired_revision
			                          THEN FALSE ELSE profile_delivery.failed_critical END,
			     desired_at=$5,
			     dispatched_at=CASE WHEN profile_delivery.desired_revision
			                             IS DISTINCT FROM EXCLUDED.desired_revision
			                        THEN NULL ELSE profile_delivery.dispatched_at END,
			     updated_at=$5`,
			tenantID, profileID, gatewayID, revision, at); err != nil {
			return fmt.Errorf("sync desired delivery: %w", err)
		}
	}
	return nil
}

// MarkRemoving moves a delivery row to REMOVING so the edge withdraws its copy.
func (s *Store) MarkRemoving(ctx context.Context, tenantID, profileID, gatewayID string, at time.Time) error {
	if _, err := s.pool.Exec(ctx,
		`UPDATE profile_delivery
		 SET state='REMOVING', updated_at=$4
		 WHERE tenant_id=$1 AND profile_id=$2 AND gateway_id=$3
		   AND state NOT IN ('REMOVING','REMOVED')`,
		tenantID, profileID, gatewayID, at); err != nil {
		return fmt.Errorf("mark removing: %w", err)
	}
	return nil
}

// DeliveriesByGateway lists the delivery rows for one gateway (excluding
// fully-removed rows) joined with the profile name and revision.
func (s *Store) DeliveriesByGateway(ctx context.Context, tenantID, gatewayID string) ([]Delivery, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+deliveryColumns+`
		 FROM profile_delivery d
		 JOIN device_profile p ON p.id = d.profile_id
		 WHERE d.tenant_id=$1 AND d.gateway_id=$2 AND d.state <> 'REMOVED'
		 ORDER BY p.name`,
		tenantID, gatewayID)
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
	}
	defer rows.Close()
	return scanDeliveries(rows)
}

// DeliveriesByProfile lists the delivery rows for one profile across gateways.
func (s *Store) DeliveriesByProfile(ctx context.Context, tenantID, profileID string) ([]Delivery, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+deliveryColumns+`
		 FROM profile_delivery d
		 JOIN device_profile p ON p.id = d.profile_id
		 WHERE d.tenant_id=$1 AND d.profile_id=$2
		 ORDER BY d.gateway_id::text`,
		tenantID, profileID)
	if err != nil {
		return nil, fmt.Errorf("list profile deliveries: %w", err)
	}
	defer rows.Close()
	return scanDeliveries(rows)
}

// MarkDispatched stamps DISPATCHED after the frame is written to a live
// session. It is idempotent for an already-DISPATCHED revision.
func (s *Store) MarkDispatched(ctx context.Context, tenantID, profileID, gatewayID string, revision int64, at time.Time) error {
	if _, err := s.pool.Exec(ctx,
		`UPDATE profile_delivery
		 SET state='DISPATCHED', dispatched_revision=$4, dispatched_at=$5, updated_at=$5
		 WHERE tenant_id=$1 AND profile_id=$2 AND gateway_id=$3 AND desired_revision=$4
		   AND state IN ('PENDING','FAILED')`,
		tenantID, profileID, gatewayID, revision, at); err != nil {
		return fmt.Errorf("mark dispatched: %w", err)
	}
	return nil
}

// Report stores one edge status uplink keyed by profile name.
func (s *Store) Report(ctx context.Context, tenantID, gatewayID string, report StatusEntry, at time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE profile_delivery d
		 SET state=$4::text,
		     reason=$5::text,
		     applied_revision=CASE WHEN $4::text='APPLIED'
		                           THEN $6::bigint ELSE d.applied_revision END,
		     applied_at=CASE WHEN $4::text='APPLIED'
		                     THEN $7::timestamptz ELSE d.applied_at END,
		     pending_alarm=CASE WHEN $4::text IN ('APPLIED','REJECTED','REMOVED')
		                        THEN FALSE ELSE d.pending_alarm END,
		     failed_at=CASE WHEN $4::text IN ('FAILED','REJECTED')
		                    THEN coalesce(d.failed_at,$7::timestamptz) ELSE NULL END,
		     failed_alarm=CASE WHEN $4::text IN ('APPLIED','REMOVED')
		                       THEN FALSE ELSE d.failed_alarm END,
		     failed_critical=CASE WHEN $4::text IN ('APPLIED','REMOVED')
		                          THEN FALSE ELSE d.failed_critical END,
		     updated_at=$7::timestamptz
		 FROM device_profile p
		 WHERE d.profile_id=p.id AND d.tenant_id=$1 AND d.gateway_id=$2
		   AND p.name=$3`,
		tenantID, gatewayID, report.Name, report.Status, report.Reason,
		report.Revision, at)
	if err != nil {
		return fmt.Errorf("report delivery: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ClaimPendingForAlarm atomically marks and returns deliveries whose latest
// desired revision has spent longer than ageCutoff without an APPLIED report.
func (s *Store) ClaimPendingForAlarm(ctx context.Context, ageCutoff time.Time) ([]Delivery, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE profile_delivery d
		 SET pending_alarm=TRUE
		 FROM device_profile p
		 WHERE d.profile_id=p.id
		   AND d.state IN ('PENDING','DISPATCHED')
		   AND d.pending_alarm=FALSE
		   AND d.desired_at <= $1
		 RETURNING `+deliveryColumns,
		ageCutoff)
	if err != nil {
		return nil, fmt.Errorf("claim pending deliveries: %w", err)
	}
	defer rows.Close()
	return scanDeliveries(rows)
}

// ClaimFailedForRaise marks and returns deliveries newly reported FAILED that
// do not yet have the MAJOR alarm open.
func (s *Store) ClaimFailedForRaise(ctx context.Context) ([]Delivery, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE profile_delivery d
		 SET failed_alarm=TRUE
		 FROM device_profile p
		 WHERE d.profile_id=p.id
		   AND d.state IN ('FAILED','REJECTED') AND d.failed_alarm=FALSE
		 RETURNING `+deliveryColumns)
	if err != nil {
		return nil, fmt.Errorf("claim failed deliveries: %w", err)
	}
	defer rows.Close()
	return scanDeliveries(rows)
}

// ClaimFailedForEscalation marks and returns deliveries whose failure has
// persisted longer than ageCutoff and are not yet CRITICAL.
func (s *Store) ClaimFailedForEscalation(ctx context.Context, ageCutoff time.Time) ([]Delivery, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE profile_delivery d
		 SET failed_critical=TRUE
		 FROM device_profile p
		 WHERE d.profile_id=p.id
		   AND d.state IN ('FAILED','REJECTED') AND d.failed_alarm=TRUE
		   AND d.failed_critical=FALSE AND d.failed_at <= $1
		 RETURNING `+deliveryColumns,
		ageCutoff)
	if err != nil {
		return nil, fmt.Errorf("claim failed-escalation deliveries: %w", err)
	}
	defer rows.Close()
	return scanDeliveries(rows)
}

// BumpRevision stamps a new content revision and projection hash on a profile
// when the rendered projection changed. It returns the effective revision.
func (s *Store) BumpRevision(ctx context.Context, tenantID, profileID, desiredHash string, at time.Time) (int64, error) {
	var revision int64
	err := s.pool.QueryRow(ctx,
		`UPDATE device_profile
		 SET revision=revision+1, desired_hash=$3, updated_at=$4
		 WHERE tenant_id=$1 AND id=$2 AND desired_hash IS DISTINCT FROM $3
		 RETURNING revision`,
		tenantID, profileID, desiredHash, at).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		// Hash unchanged: return the current revision without bumping.
		if err := s.pool.QueryRow(ctx,
			`SELECT revision FROM device_profile WHERE tenant_id=$1 AND id=$2`,
			tenantID, profileID).Scan(&revision); err != nil {
			return 0, fmt.Errorf("read profile revision: %w", err)
		}
		return revision, nil
	}
	if err != nil {
		return 0, fmt.Errorf("bump profile revision: %w", err)
	}
	return revision, nil
}

func scanDeliveries(rows pgx.Rows) ([]Delivery, error) {
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.TenantID, &d.ProfileID, &d.ProfileName, &d.GatewayID,
			&d.DesiredRevision, &d.DispatchedRevision, &d.AppliedRevision,
			&d.State, &d.Reason, &d.DesiredAt, &d.DispatchedAt, &d.AppliedAt,
			&d.FailedAt, &d.UpdatedAt, &d.Explicit); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GatewayFormats reads the render formats a gateway declared in its last
// hello. An empty result means "no preference declared" and the neutral
// format is used.
func (s *Store) GatewayFormats(ctx context.Context, tenantID, gatewayID string) ([]string, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT agent_profile_formats FROM device
		 WHERE tenant_id=$1 AND id=$2`,
		tenantID, gatewayID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read gateway formats: %w", err)
	}
	out := []string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out, nil
}

// ServiceNameForProtocol maps an open protocol family to the device-service
// name in the global catalog (ADR-0033). It is a convenience hint for the
// frame's opaque target; an unknown family returns "".
func (s *Store) ServiceNameForProtocol(ctx context.Context, protocol string) (string, error) {
	if protocol == "" {
		return "", nil
	}
	var name string
	err := s.pool.QueryRow(ctx,
		`SELECT service_name FROM protocol_catalog WHERE family=$1`,
		protocol).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("lookup protocol service: %w", err)
	}
	return name, nil
}

// scanStrings reads a single text column into a slice.
func scanStrings(rows pgx.Rows) ([]string, error) {
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
