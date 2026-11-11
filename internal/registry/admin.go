// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/identity"
	"github.com/nereidai/nereidlink/internal/rbac"
)

// queryRunner supports transactional or pool-backed row lookup.
type queryRunner interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Admin is the registry management service.
type Admin struct {
	pool *pgxpool.Pool

	// presenceEvictor force-removes presence during lifecycle changes.
	presenceEvictor PresenceEvictor

	// deviceDisconnector closes live transport clients when credentials
	// are invalidated.
	deviceDisconnector DeviceDisconnector

	// alarmCloser closes live alarms during transfer.
	alarmCloser DeviceAlarmCloser

	// alarmRaiser raises credential expiry/expiration alarms.
	alarmRaiser CredentialAlarmRaiser

	// auditor appends tamper-evident credential management events.
	auditor CredentialAuditor

	// certIssuer issues X509 device material with the built-in CA and
	// wraps/unwraps stored device private keys.
	certIssuer CertificateIssuer

	// quotaGuard verifies the tenant device quota before insertion.
	quotaGuard QuotaGuard

	// connectorTemplateValidator optionally validates a profile's connector
	// config template against the edge-registered JSON Schema for its
	// type/version. Nil leaves schema validation off (shape-only). The
	// implementation is best-effort: a type without a registered schema
	// passes, keeping unmanaged connector types usable.
	connectorTemplateValidator ConnectorTemplateValidator

	// attributePublisher fans server/shared attribute changes out to a
	// live device session. Optional; nil makes writes persistence-only.
	attributePublisher AttributePublisher

	// attributeEventPublisher fans every committed attribute change to the
	// console-plane WebSocket router. Optional; nil keeps persistence-only.
	attributeEventPublisher AttributeEventPublisher

	// commandMaterializer reconciles automatic command-catalog entries
	// (read commands) from a device's bound profile. Optional; nil leaves
	// catalog materialization off. Declared locally to avoid a registry ->
	// command import cycle.
	commandMaterializer CommandMaterializer

	// credentialCacheInvalidator publishes cache eviction events when a
	// credential is rotated, disabled or revoked, so cached decisions
	// converge immediately instead of waiting for their TTL. Optional.
	credentialCacheInvalidator CredentialCacheInvalidator
}

// CredentialCacheInvalidator notifies credential caches of changed
// credentials.
type CredentialCacheInvalidator interface {
	// CredentialInvalidated evicts one credential from every cache.
	CredentialInvalidated(ctx context.Context, credentialsID string) error
	// EpochInvalidated fences every cached credential of one tenant.
	EpochInvalidated(ctx context.Context, tenantID string) error
}

// SetCredentialCacheInvalidator wires the cache eviction publisher.
func (a *Admin) SetCredentialCacheInvalidator(i CredentialCacheInvalidator) {
	a.credentialCacheInvalidator = i
}

// CommandMaterializer reconciles per-device automatic command catalog
// entries from profile descriptors. *command.Store implements it.
type CommandMaterializer interface {
	MaterializeDevice(ctx context.Context, tenantID, deviceID, profileID string) error
	MaterializeProfile(ctx context.Context, tenantID, profileID string) error
}

// QuotaGuard checks that creating one more device stays within the tenant
// device quota.
type QuotaGuard interface {
	CheckDevice(ctx context.Context, tenantID string) error
}

// ConnectorTemplateValidator validates a connector config document
// against the catalog schema registered for one connector type/version.
// Satisfied by *connectorcatalog.Service, declared locally to avoid an
// import cycle (connector -> registry would be circular).
type ConnectorTemplateValidator interface {
	// ValidateConfig validates a full, self-contained config document.
	ValidateConfig(ctx context.Context, tenantID, connectorType, version string, config json.RawMessage) error
	// ValidateOverride validates a reusable template document: presence
	// constraints are relaxed because per-instance keys (e.g. an EdgeX
	// device_name) are injected at instance-create time, while the type
	// constraints on the keys the template sets are still enforced.
	ValidateOverride(ctx context.Context, tenantID, connectorType, version string, config json.RawMessage) error
	// ResolveVersion mirrors the connector service's plugin-version
	// selection: a blank version resolves to the highest registered one.
	ResolveVersion(ctx context.Context, tenantID, connectorType, version string) (string, error)
}

// SetConnectorTemplateValidator wires the optional JSON-Schema validator
// used when persisting profiles that carry a connector template.
func (a *Admin) SetConnectorTemplateValidator(v ConnectorTemplateValidator) {
	a.connectorTemplateValidator = v
}

// SetCommandMaterializer wires the optional automatic command catalog
// reconciler (profile read/write points -> per-device command whitelist).
func (a *Admin) SetCommandMaterializer(m CommandMaterializer) {
	a.commandMaterializer = m
}

// materializeCommands best-effort reconciles automatic catalog entries.
// Errors are logged only: command whitelisting must never break device or
// profile management when optional infrastructure is unavailable.
func (a *Admin) materializeCommands(ctx context.Context, fn func(context.Context) error) {
	if a.commandMaterializer == nil {
		return
	}
	if err := fn(ctx); err != nil {
		slog.Warn("automatic command catalog materialization failed", "error", err)
	}
}

// SetQuotaGuard wires the device quota guard.
func (a *Admin) SetQuotaGuard(guard QuotaGuard) {
	a.quotaGuard = guard
}

// NewAdmin constructs a registry management service.
func NewAdmin(pool *pgxpool.Pool) *Admin {
	return &Admin{pool: pool}
}

// ListProfiles returns profiles and descriptors for one tenant.
func (a *Admin) ListProfiles(ctx context.Context, tenantID string) ([]ProfileDTO, error) {
	rows, err := a.pool.Query(ctx,
		`SELECT `+profileColumns+`
		 FROM device_profile
		 WHERE tenant_id = $1
		 ORDER BY name`,
		tenantID)
	if err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}
	defer rows.Close()

	profiles := []ProfileDTO{}
	for rows.Next() {
		profile, err := scanProfile(rows)
		if err != nil {
			return nil, fmt.Errorf("scan profile: %w", err)
		}
		descriptors, err := a.descriptorsFor(ctx, profile.ID)
		if err != nil {
			return nil, err
		}
		profile.Descriptors = descriptors
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

// CreateProfile creates a profile and its descriptor set.
func (a *Admin) CreateProfile(ctx context.Context, tenantID string, input ProfileDTO) (ProfileDTO, error) {
	if input.Descriptors == nil {
		input.Descriptors = []DescriptorDTO{}
	}
	// Emit the template documents as {}/[] rather than null on the create
	// response; the columns default the same way.
	if input.ConnectorConfigTemplate == nil {
		input.ConnectorConfigTemplate = json.RawMessage("{}")
	}
	if input.ConnectorMappings == nil {
		input.ConnectorMappings = json.RawMessage("[]")
	}
	if err := validateProfile(input); err != nil {
		return ProfileDTO{}, err
	}
	if err := a.validateConnectorTemplateSchema(ctx, tenantID, input); err != nil {
		return ProfileDTO{}, err
	}
	if err := ensureChainGroup(ctx, a.pool, tenantID, input.RuleChainGroupID); err != nil {
		return ProfileDTO{}, err
	}
	input.ID = uuid.NewString()

	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return ProfileDTO{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var chainGroupID any
	if input.RuleChainGroupID != "" {
		chainGroupID = input.RuleChainGroupID
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO device_profile
		    (id, tenant_id, name, rule_chain_group_id,
		     connector_type, connector_type_version,
		     connector_config_template, connector_mappings_template,
		     protocol)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		input.ID, tenantID, input.Name, chainGroupID,
		input.ConnectorType, input.ConnectorTypeVersion,
		normalizeProfileTemplate(input.ConnectorConfigTemplate, "{}"),
		normalizeProfileTemplate(input.ConnectorMappings, "[]"),
		input.Protocol); err != nil {
		return ProfileDTO{}, mapSQLError(err)
	}
	for i := range input.Descriptors {
		if input.Descriptors[i].ID == "" {
			input.Descriptors[i].ID = uuid.NewString()
		}
		input.Descriptors[i].Version = 1
		if err := insertDescriptor(ctx, tx, tenantID, &input.Descriptors[i], input.ID, 1, true, true); err != nil {
			return ProfileDTO{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ProfileDTO{}, mapSQLError(err)
	}
	return input, nil
}

// GetProfile returns one profile with descriptors.
func (a *Admin) GetProfile(ctx context.Context, tenantID, profileID string) (ProfileDTO, error) {
	profile, err := scanProfile(a.pool.QueryRow(ctx,
		`SELECT `+profileColumns+`
		 FROM device_profile
		 WHERE id = $1 AND tenant_id = $2`,
		profileID, tenantID))
	if err != nil {
		return ProfileDTO{}, mapSQLError(err)
	}
	descriptors, err := a.descriptorsFor(ctx, profile.ID)
	if err != nil {
		return ProfileDTO{}, err
	}
	profile.Descriptors = descriptors
	return profile, nil
}

// GetProfileByName returns one tenant profile identified by its unique name.
// It backs edge-profile adoption and library auto-ingest, which resolve a
// same-name native profile before deciding to create or to detect drift.
func (a *Admin) GetProfileByName(ctx context.Context, tenantID, name string) (ProfileDTO, error) {
	profile, err := scanProfile(a.pool.QueryRow(ctx,
		`SELECT `+profileColumns+`
		 FROM device_profile
		 WHERE tenant_id = $1 AND name = $2`,
		tenantID, name))
	if err != nil {
		return ProfileDTO{}, mapSQLError(err)
	}
	descriptors, err := a.descriptorsFor(ctx, profile.ID)
	if err != nil {
		return ProfileDTO{}, err
	}
	profile.Descriptors = descriptors
	return profile, nil
}

// UpdateProfile updates a profile and replaces its descriptor set.
func (a *Admin) UpdateProfile(ctx context.Context, tenantID, profileID string, input ProfileDTO) (ProfileDTO, error) {
	if input.Descriptors == nil {
		input.Descriptors = []DescriptorDTO{}
	}
	if input.ConnectorConfigTemplate == nil {
		input.ConnectorConfigTemplate = json.RawMessage("{}")
	}
	if input.ConnectorMappings == nil {
		input.ConnectorMappings = json.RawMessage("[]")
	}
	if err := validateProfile(input); err != nil {
		return ProfileDTO{}, err
	}
	if err := a.validateConnectorTemplateSchema(ctx, tenantID, input); err != nil {
		return ProfileDTO{}, err
	}
	if err := ensureChainGroup(ctx, a.pool, tenantID, input.RuleChainGroupID); err != nil {
		return ProfileDTO{}, err
	}
	current, err := a.GetProfile(ctx, tenantID, profileID)
	if err != nil {
		return ProfileDTO{}, err
	}

	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return ProfileDTO{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var chainGroupID any
	if input.RuleChainGroupID != "" {
		chainGroupID = input.RuleChainGroupID
	}
	// Spec-field ownership (ADR-0032 §5, revised): the protocol family and
	// each point's connector address are operator-maintainable on EVERY
	// profile source. An edge/import report frequently omits the protocol
	// family and concrete device/address binding (EdgeX device services ship
	// no pre-provisioned device), so the console must be able to fill those in
	// by hand. The vendor-owned spec columns (data_type/read_write/
	// edge_default/edge_min/edge_max/inactive) remain editable only on a
	// console-authored (manual) profile; on edge/import profiles they stay
	// owned by syncEdgeDescriptors/ImportEdgeProfile and a form echo of them
	// cannot clobber the vendor spec.
	writeEdgeSpec := current.Source == SourceManual || current.Source == ""

	if _, err := tx.Exec(ctx,
		`UPDATE device_profile
		 SET name = $1, rule_chain_group_id = $4, updated_at = now(),
		     connector_type = $5, connector_type_version = $6,
		     connector_config_template = $7, connector_mappings_template = $8,
		     protocol = $9
		 WHERE id = $2 AND tenant_id = $3`,
		input.Name, profileID, tenantID, chainGroupID,
		input.ConnectorType, input.ConnectorTypeVersion,
		normalizeProfileTemplate(input.ConnectorConfigTemplate, "{}"),
		normalizeProfileTemplate(input.ConnectorMappings, "[]"),
		input.Protocol); err != nil {
		return ProfileDTO{}, mapSQLError(err)
	}

	old := map[string]DescriptorDTO{}
	for _, descriptor := range current.Descriptors {
		if !writeEdgeSpec {
			// The form for an edge/import profile echoes but cannot edit the
			// vendor-owned spec columns, so ignore them when deciding whether
			// the operator revision actually changed: an echo of the vendor
			// spec must not bump the descriptor version. Address is NOT
			// stripped because the operator may maintain it on any source.
			descriptor = stripEdgeSpecColumns(descriptor)
		}
		old[descriptor.ID] = descriptor
	}
	seen := map[string]bool{}
	for i := range input.Descriptors {
		descriptor := &input.Descriptors[i]
		version := 1
		if previous, ok := old[descriptor.ID]; ok && descriptor.ID != "" {
			seen[descriptor.ID] = true
			version = previous.Version
			candidate := *descriptor
			if !writeEdgeSpec {
				candidate = stripEdgeSpecColumns(candidate)
			}
			if descriptorChanged(previous, candidate) {
				version++
			}
		}
		if descriptor.ID == "" {
			descriptor.ID = uuid.NewString()
		}
		descriptor.Version = version
		if err := insertDescriptor(ctx, tx, tenantID, descriptor, profileID, version, false, writeEdgeSpec); err != nil {
			return ProfileDTO{}, err
		}
	}
	for id := range old {
		if !seen[id] {
			if _, err := tx.Exec(ctx,
				`DELETE FROM measurement_descriptor WHERE id = $1`, id); err != nil {
				return ProfileDTO{}, err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ProfileDTO{}, mapSQLError(err)
	}
	// Reconcile automatic command catalog rows of every ACTIVE device
	// bound to the edited profile. Best-effort.
	a.materializeCommands(ctx, func(ctx context.Context) error {
		return a.commandMaterializer.MaterializeProfile(ctx, tenantID, profileID)
	})
	input.ID = profileID
	return input, nil
}

// DeleteProfile deletes a profile and detaches its devices.
func (a *Admin) DeleteProfile(ctx context.Context, tenantID, profileID string) error {
	// Capture bound devices before the FK detaches them so their automatic
	// command entries can be pruned afterwards.
	var deviceIDs []string
	if a.commandMaterializer != nil {
		rows, err := a.pool.Query(ctx,
			`SELECT id::text FROM device
			 WHERE tenant_id = $1 AND profile_id = $2`,
			tenantID, profileID)
		if err != nil {
			return mapSQLError(err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			deviceIDs = append(deviceIDs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	tag, err := a.pool.Exec(ctx,
		`DELETE FROM device_profile WHERE id = $1 AND tenant_id = $2`,
		profileID, tenantID)
	if err != nil {
		return mapSQLError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	for _, deviceID := range deviceIDs {
		a.materializeCommands(ctx, func(ctx context.Context) error {
			return a.commandMaterializer.MaterializeDevice(ctx, tenantID, deviceID, "")
		})
	}
	return nil
}

// DeviceFilter narrows a paged device listing. Empty fields are ignored.
type DeviceFilter struct {
	// Search matches canonical_name case-insensitively as a substring.
	Search string

	// ProfileID filters by assigned device profile.
	ProfileID string

	// CustomerID filters by assigned customer.
	CustomerID string

	// GroupID filters by assigned device group.
	GroupID string

	// State filters by lifecycle state; DECOMMISSIONED devices are
	// excluded unless explicitly requested.
	State string

	// GatewayOnly filters to devices flagged as gateways. It backs the
	// sub-device binding picker in the UI.
	GatewayOnly bool

	// ManagedByGatewayID filters to sub-devices bound to one gateway. It
	// backs the "managed devices" view of a gateway.
	ManagedByGatewayID string

	// Type filters by the device kind shown in the console Type column:
	// "gateway", "subdevice" (non-gateway bound to a gateway) or "device"
	// (standalone, not a gateway and unbound). Empty means any type.
	Type string

	// Connection filters by the latest presence boundary: "online",
	// "offline", or "unknown" (no boundary ever recorded). Empty means any
	// connection state. Unlike the lifecycle State it reads ts_presence.
	Connection string
}

// defaultVisibleStates are the lifecycle states the device list shows when
// the caller does not filter by state.
//
// QUARANTINED is included deliberately. Quarantine is an incident-response
// state, and an incident nobody can see is not being handled: a device hidden
// from the list looks identical to a device that was never quarantined.
// DECOMMISSIONED and ARCHIVED stay hidden because they are the normal end of
// a device's life, not an exception requiring attention.
var defaultVisibleStates = []string{StateUnclaimed, StateActive, StateQuarantined}

// StateAll is the sentinel filter value that disables lifecycle filtering.
// An empty State keeps the historical default (defaultVisibleStates, which
// hides decommissioned rows); callers that genuinely want every row — the
// console "all states" choice — must pass StateAll explicitly so background
// list consumers (claim pickers, gateways) keep hiding dead devices.
const StateAll = "ALL"

// ListDevices returns one page of devices for a tenant.
//
// The list is narrowed by the caller's rbac.DataScope carried on ctx, so a
// scoped member sees only its own customer/group subtree even though it holds
// the same permission points as a tenant admin. The count query must be
// narrowed identically, or a scoped member would learn the tenant-wide total
// and see empty pages.
func (a *Admin) ListDevices(ctx context.Context, tenantID string, filter DeviceFilter, page, pageSize int) (apipage.Page[DeviceDTO], error) {
	scope := rbac.ScopeFromContext(ctx)

	// The count query carries no LIMIT/OFFSET, so it must not be passed the
	// pagination arguments. Sending 9 arguments while the SQL only references
	// $1-$6 and $9 leaves $7 and $8 unreferenced, and PostgreSQL then cannot
	// infer a type for them (SQLSTATE 42P18). Hence the array sits at $7 here
	// while it sits at $9 in the list query below. For the same reason the
	// scope predicate is rendered against each query's own argument count.
	var total int
	countClause, countArgs := scope.DevicePredicate("", 11)
	if err := a.pool.QueryRow(ctx,
		`SELECT count(*)
		 FROM device
		 WHERE tenant_id = $1
		   AND ($2 = '' OR canonical_name ILIKE '%' || $2 || '%'
		        OR alias ILIKE '%' || $2 || '%')
		   AND ($3 = '' OR profile_id = NULLIF($3,'')::uuid)
		   AND ($5 = '' OR customer_id = NULLIF($5,'')::uuid)
		   AND ($6 = '' OR group_id = NULLIF($6,'')::uuid)
 		   AND ($4 = 'ALL'
 		        OR ($4 = '' AND state = ANY($7::text[]))
 		        OR ($4 <> '' AND $4 <> 'ALL' AND state = $4))
 		   AND ($8 = FALSE OR is_gateway)
		   AND ($9 = '' OR gateway_id = NULLIF($9,'')::uuid)
		   AND ($10 = ''
		        OR ($10 = 'gateway' AND is_gateway)
		        OR ($10 = 'subdevice' AND NOT is_gateway AND gateway_id IS NOT NULL)
		        OR ($10 = 'device' AND NOT is_gateway AND gateway_id IS NULL))
		   AND ($11 = ''
		        OR COALESCE((SELECT state FROM ts_presence
		                     WHERE device_id = device.id
		                     ORDER BY time DESC LIMIT 1), 'UNKNOWN') = UPPER($11))`+countClause,
		append([]any{
			tenantID, filter.Search, filter.ProfileID, filter.State,
			filter.CustomerID, filter.GroupID, defaultVisibleStates,
			filter.GatewayOnly, filter.ManagedByGatewayID, filter.Type,
			filter.Connection,
		}, countArgs...)...).Scan(&total); err != nil {
		return apipage.Page[DeviceDTO]{}, fmt.Errorf("count devices: %w", err)
	}

	// Last-seen times go through the plpgsql helpers from migration 0093,
	// not inline correlated subqueries: a parameterized max(time) against the
	// telemetry hypertable is planned generically and makes ChunkAppend pick
	// the time-only index, filtering millions of rows per device (see L-150).
	listClause, listArgs := scope.DevicePredicate("d", 13)
	rows, err := a.pool.Query(ctx,
		`SELECT d.id::text, d.canonical_name, d.profile_id::text,
		        COALESCE(d.rule_chain_group_id::text, ''),
		        COALESCE(d.customer_id::text, ''),
		        COALESCE(d.group_id::text, ''),
		        d.is_gateway, COALESCE(d.gateway_id::text, ''),
		        COALESCE(gw.canonical_name, ''),
		        COALESCE(d.firmware_version, ''),
		        d.state,
		        d.quarantined_at, COALESCE(d.quarantine_reason, ''),
		        COALESCE(latest.state, ''), latest.time,
		        d.created_at, d.updated_at,
		        COALESCE(d.alias, ''), COALESCE(d.notes, ''),
		        last_activity.at,
		        COALESCE(ci.id::text, ''), COALESCE(ci.connector_type, ''),
		        COALESCE(ci.state, ''),
		        (SELECT count(*)::int FROM connector_instance cg
		          WHERE cg.tenant_id = d.tenant_id AND cg.gateway_id = d.id
		            AND cg.device_id IS NULL),
		        d.agent_features,
		        d.agent_operations
		 FROM device d
		 LEFT JOIN device gw ON gw.id = d.gateway_id
		 LEFT JOIN connector_instance ci
		   ON ci.tenant_id = d.tenant_id AND ci.device_id = d.id
		 LEFT JOIN LATERAL (
		     SELECT state, time
		     FROM ts_presence
		     WHERE device_id = d.id
		     ORDER BY time DESC
		     LIMIT 1
		 ) latest ON true
		 LEFT JOIN LATERAL (
		     SELECT last_telemetry_time(d.id) AS t
		 ) tele ON true
		 LEFT JOIN LATERAL (
		     SELECT GREATEST(latest.time, tele.t) AS at
		 ) last_activity ON true
		 WHERE d.tenant_id = $1
		   AND ($2 = '' OR d.canonical_name ILIKE '%' || $2 || '%'
		        OR d.alias ILIKE '%' || $2 || '%')
		   AND ($3 = '' OR d.profile_id = NULLIF($3,'')::uuid)
		   AND ($5 = '' OR d.customer_id = NULLIF($5,'')::uuid)
		   AND ($6 = '' OR d.group_id = NULLIF($6,'')::uuid)
		   AND ($4 = 'ALL'
		        OR ($4 = '' AND d.state = ANY($9::text[]))
		        OR ($4 <> '' AND $4 <> 'ALL' AND d.state = $4))
		   AND ($10 = FALSE OR d.is_gateway)
		   AND ($11 = '' OR d.gateway_id = NULLIF($11,'')::uuid)
		   AND ($12 = ''
		        OR ($12 = 'gateway' AND d.is_gateway)
		        OR ($12 = 'subdevice' AND NOT d.is_gateway AND d.gateway_id IS NOT NULL)
		        OR ($12 = 'device' AND NOT d.is_gateway AND d.gateway_id IS NULL))
		   AND ($13 = ''
		        OR COALESCE(latest.state, 'UNKNOWN') = UPPER($13))`+listClause+`
		 ORDER BY d.created_at DESC, d.canonical_name
		 LIMIT $7 OFFSET $8`,
		append([]any{
			tenantID, filter.Search, filter.ProfileID, filter.State,
			filter.CustomerID, filter.GroupID, pageSize, (page - 1) * pageSize,
			defaultVisibleStates, filter.GatewayOnly, filter.ManagedByGatewayID, filter.Type,
			filter.Connection,
		}, listArgs...)...)

	if err != nil {
		return apipage.Page[DeviceDTO]{}, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()

	devices := []DeviceDTO{}
	for rows.Next() {
		var device DeviceDTO
		var profileID *string
		var presenceState *string
		var presenceSince *time.Time
		var createdAt, updatedAt time.Time
		var alias, notes string
		var lastActivity *time.Time
		var quarantinedAt *time.Time
		if err := rows.Scan(&device.ID, &device.CanonicalName, &profileID,
			&device.RuleChainGroupID, &device.CustomerID, &device.GroupID,
			&device.IsGateway, &device.GatewayID, &device.GatewayName,
			&device.FirmwareVersion,
			&device.State, &quarantinedAt, &device.QuarantineReason,
			&presenceState, &presenceSince, &createdAt, &updatedAt,
			&alias, &notes, &lastActivity,
			&device.ConnectorID, &device.ConnectorType, &device.ConnectorState,
			&device.GatewayConnectorCount, &device.AgentFeatures,
			&device.AgentOperations); err != nil {
			return apipage.Page[DeviceDTO]{}, fmt.Errorf("scan device: %w", err)
		}
		device.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		device.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		device.Alias = alias
		device.Notes = notes
		if lastActivity != nil {
			device.LastActivityAt = lastActivity.UTC().Format(time.RFC3339)
		}
		if profileID != nil {
			device.ProfileID = *profileID
		}
		// The quarantine columns travel with the row so the list can render
		// the incident inline, not only as a bare state badge.
		if quarantinedAt != nil {
			device.QuarantinedAt = quarantinedAt.UTC().Format(time.RFC3339)
		}
		if presenceState != nil && *presenceState != "" {
			device.PresenceState = *presenceState
			if presenceSince != nil {
				device.PresenceSince = presenceSince.UTC().Format(time.RFC3339)
			}
		}
		devices = append(devices, device)
	}
	if err := rows.Err(); err != nil {
		return apipage.Page[DeviceDTO]{}, err
	}
	return apipage.New(devices, total, page, pageSize), nil
}

// CreateDevice creates one device. Devices default to ACTIVE; a device
// may be pre-registered as UNCLAIMED for the installer claiming flow.
// prepareDeviceCreate runs every validation that must pass before a device
// row is inserted. It is shared by CreateDevice and the import preview so
// the dry-run reports exactly what creation would reject.
func (a *Admin) prepareDeviceCreate(ctx context.Context, tenantID string, input DeviceDTO) error {
	if err := validateDevice(input); err != nil {
		return err
	}
	if a.quotaGuard != nil {
		if err := a.quotaGuard.CheckDevice(ctx, tenantID); err != nil {
			return err
		}
	}
	if input.ProfileID != "" {
		if err := ensureProfile(ctx, a.pool, tenantID, input.ProfileID); err != nil {
			return err
		}
	}
	if err := ensureChainGroup(ctx, a.pool, tenantID, input.RuleChainGroupID); err != nil {
		return err
	}
	if err := a.validateOwnershipTarget(ctx, tenantID, input.CustomerID, input.GroupID); err != nil {
		return err
	}
	if input.IsGateway && input.GatewayID != "" {
		return invalid("a gateway device cannot reference another gateway")
	}
	if input.GatewayID != "" {
		if err := ensureGateway(ctx, a.pool, tenantID, input.GatewayID); err != nil {
			return err
		}
	}
	state := input.State
	if state == "" {
		state = StateActive
	}
	if state != StateActive && state != StateUnclaimed {
		return invalid("state must be ACTIVE or UNCLAIMED on creation")
	}
	return nil
}

func (a *Admin) CreateDevice(ctx context.Context, tenantID string, input DeviceDTO) (DeviceDTO, error) {
	if err := a.prepareDeviceCreate(ctx, tenantID, input); err != nil {
		return DeviceDTO{}, err
	}

	state := input.State
	if state == "" {
		state = StateActive
	}

	input.ID = uuid.NewString()
	var profileID any
	if input.ProfileID != "" {
		profileID = input.ProfileID
	}
	var chainGroupID any
	if input.RuleChainGroupID != "" {
		chainGroupID = input.RuleChainGroupID
	}
	if _, err := a.pool.Exec(ctx,
		`INSERT INTO device
		   (id, tenant_id, canonical_name, profile_id, rule_chain_group_id,
		    customer_id, group_id, is_gateway, gateway_id, state, alias, notes)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6,'')::uuid, NULLIF($7,'')::uuid,
		         $8, NULLIF($9,'')::uuid, $10, NULLIF($11,''), NULLIF($12,''))`,
		input.ID, tenantID, input.CanonicalName, profileID, chainGroupID,
		input.CustomerID, input.GroupID, input.IsGateway, input.GatewayID, state,
		input.Alias, input.Notes); err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}

	if err := a.recordRegistration(ctx, tenantID, input.ID, input); err != nil {
		return DeviceDTO{}, err
	}
	// Backfill automatic command-catalog entries from the bound profile's
	// readable/writable points. Best-effort: a materialization failure never
	// blocks device creation (operators can still declare commands manually).
	a.materializeCommands(ctx, func(ctx context.Context) error {
		return a.commandMaterializer.MaterializeDevice(ctx, tenantID, input.ID, input.ProfileID)
	})
	return a.loadDevice(ctx, tenantID, input.ID)
}

// recordRegistration writes the registration ownership history row. A
// failure removes the freshly created device so the two never diverge.
func (a *Admin) recordRegistration(ctx context.Context, tenantID, deviceID string, input DeviceDTO) error {
	details := map[string]any{
		"state":      input.State,
		"is_gateway": input.IsGateway,
	}
	if input.State == "" {
		details["state"] = StateActive
	}
	if _, err := a.pool.Exec(ctx,
		`INSERT INTO device_ownership_event
		   (id, tenant_id, device_id, event_type,
		    to_customer_id, to_group_id, details)
		 VALUES ($1, $2, $3, $4,
		         NULLIF($5,'')::uuid, NULLIF($6,'')::uuid, $7::jsonb)`,
		uuid.NewString(), tenantID, deviceID, OwnershipRegistered,
		input.CustomerID, input.GroupID, mustMarshalDetails(details)); err != nil {
		if _, cleanupErr := a.pool.Exec(ctx,
			`DELETE FROM device WHERE id = $1 AND tenant_id = $2`,
			deviceID, tenantID); cleanupErr != nil {
			return fmt.Errorf("record registration: %w (cleanup also failed: %v)",
				mapSQLError(err), cleanupErr)
		}
		return fmt.Errorf("record registration: %w", mapSQLError(err))
	}
	return nil
}

// mustMarshalDetails encodes history details as a JSON string.
func mustMarshalDetails(details map[string]any) string {
	raw, err := json.Marshal(details)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// GetDevice returns one tenant-owned device.
func (a *Admin) GetDevice(ctx context.Context, tenantID, deviceID string) (DeviceDTO, error) {
	return deviceRow(ctx, a.pool, tenantID, deviceID)
}

// StampGatewayFeatures records the feature set negotiated in the gateway's
// last successful hello (ADR-0034). Only gateway rows are updated; the
// stored value is the enabled (intersected) feature list, never the raw
// edge advertisement.
func (a *Admin) StampGatewayFeatures(ctx context.Context, tenantID, gatewayID string, features []string) error {
	if len(features) == 0 {
		features = []string{}
	}
	tag, err := a.pool.Exec(ctx,
		`UPDATE device
		 SET agent_features=$3, updated_at=now()
		 WHERE id=$1 AND tenant_id=$2 AND is_gateway=TRUE`,
		gatewayID, tenantID, features)
	if err != nil {
		return fmt.Errorf("stamp gateway features: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// StampGatewayOperations records the operation keys the gateway advertised
// in its last successful hello (ADR-0035). Only gateway rows are updated.
func (a *Admin) StampGatewayOperations(ctx context.Context, tenantID, gatewayID string, operations []string) error {
	if len(operations) == 0 {
		operations = []string{}
	}
	tag, err := a.pool.Exec(ctx,
		`UPDATE device
		 SET agent_operations=$3, updated_at=now()
		 WHERE id=$1 AND tenant_id=$2 AND is_gateway=TRUE`,
		gatewayID, tenantID, operations)
	if err != nil {
		return fmt.Errorf("stamp gateway operations: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// StampGatewayProfileFormats records the profile document formats enabled for
// a gateway's last successful hello (ADR-0038). Only gateway rows are updated;
// the stored value is the negotiated intersection, never the raw edge
// advertisement.
func (a *Admin) StampGatewayProfileFormats(ctx context.Context, tenantID, gatewayID string, formats []string) error {
	if len(formats) == 0 {
		formats = []string{}
	}
	raw, err := json.Marshal(formats)
	if err != nil {
		return fmt.Errorf("encode gateway profile formats: %w", err)
	}
	tag, err := a.pool.Exec(ctx,
		`UPDATE device
		 SET agent_profile_formats=$3, updated_at=now()
		 WHERE id=$1 AND tenant_id=$2 AND is_gateway=TRUE`,
		gatewayID, tenantID, raw)
	if err != nil {
		return fmt.Errorf("stamp gateway profile formats: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// StampGatewayInference records the accepted edge model-inference capability
// for a gateway's last successful hello (ADR-0039): the contract version and
// the intersected backend protocol families. A nil advert (feature not
// negotiated or rejected) is stamped as an empty object, so the runtime stale
// watchdog only considers gateways that actually accepted inference.
func (a *Admin) StampGatewayInference(ctx context.Context, tenantID, gatewayID,
	contract string, backends []string) error {
	capability := map[string]any{}
	if contract != "" {
		capability["contract"] = contract
		if len(backends) > 0 {
			capability["backends"] = backends
		}
	}
	raw, err := json.Marshal(capability)
	if err != nil {
		return fmt.Errorf("encode gateway inference capability: %w", err)
	}
	tag, err := a.pool.Exec(ctx,
		`UPDATE device
		 SET agent_inference=$3, updated_at=now()
		 WHERE id=$1 AND tenant_id=$2 AND is_gateway=TRUE`,
		gatewayID, tenantID, raw)
	if err != nil {
		return fmt.Errorf("stamp gateway inference: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (a *Admin) UpdateDevice(ctx context.Context, tenantID, deviceID string, input DeviceDTO) (DeviceDTO, error) {
	if err := validateDevice(input); err != nil {
		return DeviceDTO{}, err
	}
	state, err := a.deviceState(ctx, a.pool, tenantID, deviceID)
	if err != nil {
		return DeviceDTO{}, err
	}
	if state != StateActive && state != StateUnclaimed {
		return DeviceDTO{}, fmt.Errorf("%w: only ACTIVE or UNCLAIMED devices can be updated", ErrConflict)
	}
	if input.ProfileID != "" {
		if err := ensureProfile(ctx, a.pool, tenantID, input.ProfileID); err != nil {
			return DeviceDTO{}, err
		}
	}
	if err := ensureChainGroup(ctx, a.pool, tenantID, input.RuleChainGroupID); err != nil {
		return DeviceDTO{}, err
	}
	if input.IsGateway && input.GatewayID != "" {
		return DeviceDTO{}, invalid("a gateway device cannot reference another gateway")
	}
	if input.GatewayID != "" {
		if err := ensureGateway(ctx, a.pool, tenantID, input.GatewayID); err != nil {
			return DeviceDTO{}, err
		}
	}
	var chainGroupID any
	if input.RuleChainGroupID != "" {
		chainGroupID = input.RuleChainGroupID
	}
	if _, err := a.pool.Exec(ctx,
		`UPDATE device
		 SET canonical_name = $1, profile_id = $2, rule_chain_group_id = $5,
		     is_gateway = $6, gateway_id = NULLIF($7,'')::uuid,
		     alias = NULLIF($8,''), notes = NULLIF($9,''),
		     updated_at = now()
		 WHERE id = $3 AND tenant_id = $4`,
		input.CanonicalName, profileIDOrNil(input.ProfileID), deviceID, tenantID, chainGroupID,
		input.IsGateway, input.GatewayID, input.Alias, input.Notes); err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}
	// Profile rebind (or detach with an empty id) recomputes automatic
	// command catalog entries. Best-effort: failure does not uncommit the
	// device update.
	a.materializeCommands(ctx, func(ctx context.Context) error {
		return a.commandMaterializer.MaterializeDevice(ctx, tenantID, deviceID, input.ProfileID)
	})
	return a.loadDevice(ctx, tenantID, deviceID)
}

// profileIDOrNil converts an empty profile id to nil.
func profileIDOrNil(profileID string) any {
	if profileID == "" {
		return nil
	}
	return profileID
}

// DeleteDevice is retained for API compatibility but performs the
// non-destructive decommission transition rather than physically
// deleting the row or its time series (ADR-0013/ADR-0017).
func (a *Admin) DeleteDevice(ctx context.Context, tenantID, deviceID string) error {
	_, err := a.DecommissionDevice(ctx, tenantID, deviceID, defaultRecallWindow, "")
	return err
}

// ListCredentials returns non-secret credential metadata for a device.
func (a *Admin) ListCredentials(ctx context.Context, tenantID, deviceID string) ([]CredentialDTO, error) {
	if _, err := a.GetDevice(ctx, tenantID, deviceID); err != nil {
		return nil, err
	}
	rows, err := a.pool.Query(ctx,
		`SELECT id::text, type, credentials_id, status,
		        COALESCE(effective_at, to_timestamp(0)),
		        effective_at IS NULL,
		        COALESCE(expires_at, to_timestamp(0)),
		        expires_at IS NULL,
		        warn_level, extra
		 FROM device_credential
		 WHERE device_id = $1
		 ORDER BY created_at`,
		deviceID)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	defer rows.Close()

	credentials := []CredentialDTO{}
	for rows.Next() {
		var credential CredentialDTO
		var effectiveAt, expiresAt time.Time
		var effectiveNull, expiresNull bool
		var extraJSON []byte
		if err := rows.Scan(&credential.ID, &credential.Type,
			&credential.CredentialsID, &credential.Status,
			&effectiveAt, &effectiveNull,
			&expiresAt, &expiresNull,
			&credential.WarnLevel, &extraJSON); err != nil {
			return nil, fmt.Errorf("scan credential: %w", err)
		}
		if len(extraJSON) > 0 && string(extraJSON) != "{}" {
			_ = json.Unmarshal(extraJSON, &credential.Extra)
		}
		if !effectiveNull {
			credential.EffectiveAt = effectiveAt.UTC().Format(time.RFC3339)
		}
		if !expiresNull {
			credential.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
		}
		credentials = append(credentials, credential)
	}
	return credentials, rows.Err()
}

// CreateCredential attaches a credential to a device.
func (a *Admin) CreateCredential(ctx context.Context, tenantID, deviceID string, input CredentialDTO, actorID, ip string) (CredentialDTO, error) {
	if err := validateCredential(input); err != nil {
		return CredentialDTO{}, err
	}
	if _, err := a.GetDevice(ctx, tenantID, deviceID); err != nil {
		return CredentialDTO{}, err
	}
	if input.Status == "" {
		input.Status = CredentialActive
	}
	// generatedToken marks a server-minted TOKEN secret, whose
	// plaintext is surfaced in the create response.
	generatedToken := false
	if input.Type == "TOKEN" && input.CredentialsValue == "" {
		raw := make([]byte, tokenSecretBytes)
		if _, err := rand.Read(raw); err != nil {
			return CredentialDTO{}, fmt.Errorf("generate token: %w", err)
		}
		input.CredentialsValue = base64.RawURLEncoding.EncodeToString(raw)
		generatedToken = true
	}
	if input.Type == "PASSWORD" && !strings.HasPrefix(input.CredentialsValue, "$argon2id$") {
		// Store PASSWORD credentials as argon2id PHC so the database
		// never holds a usable plaintext password.
		hashed, err := identity.HashSecret(input.CredentialsValue)
		if err != nil {
			return CredentialDTO{}, fmt.Errorf("hash password: %w", err)
		}
		input.CredentialsValue = hashed
	}
	effectiveAt, expiresAt, err := parseCredentialWindow(input)
	if err != nil {
		return CredentialDTO{}, err
	}

	// issuedMaterial carries plaintext returned from a UI-driven X509
	// issuance; the private key is stored encrypted and re-downloadable.
	issuedMaterial := false
	if input.Type == "X509" && input.Issue {
		if a.certIssuer == nil {
			return CredentialDTO{}, invalid("platform CA issuance is not configured")
		}
		var notAfter time.Time
		if expiresAt != nil {
			notAfter = *expiresAt
		}
		certPEM, keyPEM, caPEM, issueErr := a.certIssuer.IssueDevice(
			ctx, input.CredentialsID, notAfter)
		if issueErr != nil {
			return CredentialDTO{}, issueErr
		}
		sealed, wrapErr := a.certIssuer.EncryptPrivateKey(keyPEM)
		if wrapErr != nil {
			return CredentialDTO{}, fmt.Errorf("seal device private key: %w", wrapErr)
		}
		if input.Extra == nil {
			input.Extra = map[string]any{}
		}
		input.Extra["encrypted_private_key"] = sealed
		input.CredentialsValue = string(certPEM)
		input.CertificatePEM = string(certPEM)
		input.PrivateKeyPEM = string(keyPEM)
		input.CAPEM = string(caPEM)
		issuedMaterial = true
	}

	extraJSON, err := json.Marshal(input.Extra)
	if err != nil {
		return CredentialDTO{}, fmt.Errorf("encode credential extra: %w", err)
	}
	if len(input.Extra) == 0 {
		extraJSON = []byte("{}")
	}

	// X509 reuse detection: store the leaf fingerprint so the same
	// certificate cannot be bound to a second device. The fingerprint
	// is non-sensitive; cert_fingerprint stays NULL for other types.
	var certFingerprint *string
	if input.Type == "X509" {
		cert, certErr := parseCredentialCertificate(input.CredentialsValue)
		if certErr != nil {
			return CredentialDTO{}, invalid("invalid X.509 certificate: " + certErr.Error())
		}
		fp := certFingerprintSHA256(cert.Raw)
		certFingerprint = &fp

		// Fail with a clear message when the same certificate is
		// already registered on another device. The partial unique
		// index remains the race-safe guarantee.
		var ownerID string
		lookupErr := a.pool.QueryRow(ctx,
			`SELECT d.id::text
			 FROM device_credential c
			 JOIN device d ON d.id = c.device_id
			 WHERE c.cert_fingerprint = $1
			 LIMIT 1`, fp).Scan(&ownerID)
		switch {
		case lookupErr == nil:
			return CredentialDTO{}, fmt.Errorf("%w: certificate already bound to device %s",
				ErrConflict, ownerID)
		case errors.Is(lookupErr, pgx.ErrNoRows):
			// free to use
		default:
			return CredentialDTO{}, mapSQLError(lookupErr)
		}
	}

	input.ID = uuid.NewString()
	if _, err := a.pool.Exec(ctx,
		`INSERT INTO device_credential
		   (id, device_id, type, credentials_id, credentials_value,
		    status, effective_at, expires_at, extra, cert_fingerprint)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		input.ID, deviceID, input.Type, input.CredentialsID, input.CredentialsValue,
		input.Status, effectiveAt, expiresAt, extraJSON, certFingerprint); err != nil {
		return CredentialDTO{}, mapSQLError(err)
	}
	a.recordCredentialAudit(ctx, tenantID, deviceID, "create", input.ID, ip, actorID, map[string]any{
		"type": input.Type, "status": input.Status,
	})

	if generatedToken {
		// Surface the generated secret; it stays readable through the
		// reveal endpoint after this response.
		return input, nil
	}
	if issuedMaterial {
		// Certificate, plaintext private key and CA are returned for the
		// operator to download; the private key remains downloadable
		// later via reveal because it is stored encrypted.
		return input, nil
	}
	input.CredentialsValue = ""
	return input, nil
}

// UpdateCredentialSecret replaces the secret value of one credential
// and ends the live session, forcing re-authentication with it.
func (a *Admin) UpdateCredentialSecret(ctx context.Context, tenantID, deviceID, credentialID, secret, actorID, ip string) error {
	// Gate on the scope-aware device read first so a credential on a device
	// outside the caller's data scope is not reachable at all.
	if _, err := a.GetDevice(ctx, tenantID, deviceID); err != nil {
		return err
	}
	var credType string
	if err := a.pool.QueryRow(ctx,
		`SELECT type FROM device_credential
		 WHERE id = $1 AND device_id = $2
		   AND device_id IN (SELECT id FROM device WHERE tenant_id = $3)`,
		credentialID, deviceID, tenantID).Scan(&credType); err != nil {
		return mapSQLError(err)
	}
	if credType == "PASSWORD" && !strings.HasPrefix(secret, "$argon2id$") {
		hashed, err := identity.HashSecret(secret)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		secret = hashed
	}
	var credentialsID string
	err := a.pool.QueryRow(ctx,
		`UPDATE device_credential
		 SET credentials_value = $1, updated_at = now()
		 WHERE id = $2 AND device_id = $3
		   AND device_id IN (SELECT id FROM device WHERE tenant_id = $4)
		 RETURNING credentials_id`,
		secret, credentialID, deviceID, tenantID).Scan(&credentialsID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return mapSQLError(err)
	}
	a.recordCredentialAudit(ctx, tenantID, deviceID, "update-secret", credentialID, ip, actorID, nil)
	a.notifyCredentialInvalidated(ctx, credentialsID)
	a.evictPresence(ctx, tenantID, deviceID, "credential secret updated")
	a.disconnectDevice(ctx, tenantID, deviceID)
	return nil
}

// DeleteCredential deletes one credential.
func (a *Admin) DeleteCredential(ctx context.Context, tenantID, deviceID, credentialID, actorID, ip string) error {
	// Scope-narrowed device gate, matching the other credential mutations.
	if _, err := a.GetDevice(ctx, tenantID, deviceID); err != nil {
		return err
	}
	var credentialsID string
	err := a.pool.QueryRow(ctx,
		`DELETE FROM device_credential
		 WHERE id = $1 AND device_id = $2
		   AND device_id IN (SELECT id FROM device WHERE tenant_id = $3)
		 RETURNING credentials_id`,
		credentialID, deviceID, tenantID).Scan(&credentialsID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return mapSQLError(err)
	}
	a.recordCredentialAudit(ctx, tenantID, deviceID, "delete", credentialID, ip, actorID, nil)
	a.notifyCredentialInvalidated(ctx, credentialsID)
	a.evictPresence(ctx, tenantID, deviceID, "credential deleted")
	a.disconnectDevice(ctx, tenantID, deviceID)
	return nil
}

// descriptorsFor loads descriptors for one profile.
func (a *Admin) descriptorsFor(ctx context.Context, profileID string) ([]DescriptorDTO, error) {
	rows, err := a.pool.Query(ctx,
		`SELECT id::text,
		        measurement_key,
		        unit,
		        transform_type,
		        scale,
		        offset_value,
		        breakpoints,
		        enum_map,
		        expression,
		        precision,
		        valid_min,
		        valid_max,
		        version,
		        data_type,
		        read_write,
		        address,
		        edge_default,
		        edge_min,
		        edge_max,
		        inactive,
		        expected_period_seconds,
		        tolerance_multiplier
		 FROM measurement_descriptor
		 WHERE profile_id = $1
		 ORDER BY measurement_key`,
		profileID)
	if err != nil {
		return nil, fmt.Errorf("load descriptors: %w", err)
	}
	defer rows.Close()
	return scanDescriptors(rows)
}

// profileColumns lists the device_profile columns selected by the profile
// readers, kept in sync with scanProfile.
const profileColumns = `id::text, name, COALESCE(rule_chain_group_id::text, ''),
        connector_type, connector_type_version,
        connector_config_template, connector_mappings_template,
        source, protocol, edge_payload, content_hash, availability`

// scanProfile scans one profileColumns row into a ProfileDTO.
func scanProfile(row pgx.Row) (ProfileDTO, error) {
	var profile ProfileDTO
	var edgePayload []byte
	err := row.Scan(&profile.ID, &profile.Name, &profile.RuleChainGroupID,
		&profile.ConnectorType, &profile.ConnectorTypeVersion,
		&profile.ConnectorConfigTemplate, &profile.ConnectorMappings,
		&profile.Source, &profile.Protocol, &edgePayload,
		&profile.ContentHash, &profile.Availability)
	if err != nil {
		return ProfileDTO{}, err
	}
	if len(edgePayload) > 0 {
		profile.EdgePayload = json.RawMessage(edgePayload)
	}
	return profile, nil
}

// scanDescriptors scans descriptor query rows.
func scanDescriptors(rows pgx.Rows) ([]DescriptorDTO, error) {
	descriptors := []DescriptorDTO{}
	for rows.Next() {
		var descriptor DescriptorDTO
		var breakpoints, enumMap, address []byte
		if err := rows.Scan(
			&descriptor.ID,
			&descriptor.MeasurementKey,
			&descriptor.Unit,
			&descriptor.TransformType,
			&descriptor.Scale,
			&descriptor.Offset,
			&breakpoints,
			&enumMap,
			&descriptor.Expression,
			&descriptor.Precision,
			&descriptor.ValidMin,
			&descriptor.ValidMax,
			&descriptor.Version,
			&descriptor.DataType,
			&descriptor.ReadWrite,
			&address,
			&descriptor.EdgeDefault,
			&descriptor.EdgeMin,
			&descriptor.EdgeMax,
			&descriptor.Inactive,
			&descriptor.ExpectedPeriodSeconds,
			&descriptor.ToleranceMultiplier,
		); err != nil {
			return nil, fmt.Errorf("scan descriptor: %w", err)
		}
		if len(breakpoints) > 0 {
			if err := json.Unmarshal(breakpoints, &descriptor.Breakpoints); err != nil {
				return nil, err
			}
		}
		if len(enumMap) > 0 {
			if err := json.Unmarshal(enumMap, &descriptor.EnumMap); err != nil {
				return nil, err
			}
		}
		if len(address) > 0 {
			descriptor.Address = json.RawMessage(address)
		}
		descriptors = append(descriptors, descriptor)
	}
	return descriptors, rows.Err()
}

// deviceRow loads one tenant-owned device with its lifecycle fields.
// deviceRow loads one device by id, narrowed by the caller's rbac.DataScope.
//
// This is the chokepoint for every single-device read and for the writes that
// load-then-mutate, so the scope is applied once here rather than in each
// caller. An out-of-scope device surfaces as ErrNotFound rather than a
// permission error: a 403 would confirm the id exists in the tenant and turn
// the device list into an enumeration oracle.
func deviceRow(ctx context.Context, runner queryRunner, tenantID, deviceID string) (DeviceDTO, error) {
	var device DeviceDTO
	var profileID *string
	var claimedAt, decommissionedAt, recallUntil, quarantinedAt *time.Time
	var createdAt, updatedAt time.Time
	var alias, notes string
	var lastActivity *time.Time
	scope := rbac.ScopeFromContext(ctx)
	clause, args := scope.DevicePredicate("d", 2)
	// The last-seen LATERALs call the migration-0093 plpgsql helpers rather
	// than inline max(time) subqueries to force a custom plan per device
	// (generic plans choose the wrong hypertable index; see L-150).
	err := runner.QueryRow(ctx,
		`SELECT d.id::text, d.canonical_name, d.profile_id::text,
		        COALESCE(d.rule_chain_group_id::text, ''),
		        COALESCE(d.customer_id::text, ''),
		        COALESCE(d.group_id::text, ''),
		        d.is_gateway, COALESCE(d.gateway_id::text, ''),
		        COALESCE(gw.canonical_name, ''),
		        COALESCE(d.firmware_version, ''),
		        d.state, d.claimed_at, d.decommissioned_at, d.recall_until,
		        d.quarantined_at, COALESCE(d.quarantine_reason, ''),
		        d.created_at, d.updated_at,
		        COALESCE(d.alias, ''), COALESCE(d.notes, ''),
		        last_activity.at,
		        COALESCE(ci.id::text, ''), COALESCE(ci.connector_type, ''),
		        COALESCE(ci.state, ''),
		        (SELECT count(*)::int FROM connector_instance cg
		          WHERE cg.tenant_id = d.tenant_id AND cg.gateway_id = d.id
		            AND cg.device_id IS NULL),
		        d.agent_features,
		        d.agent_operations
		 FROM device d
		 LEFT JOIN device gw ON gw.id = d.gateway_id
		 LEFT JOIN connector_instance ci
		   ON ci.tenant_id = d.tenant_id AND ci.device_id = d.id
		 LEFT JOIN LATERAL (
		     SELECT last_presence_time(d.id) AS t
		 ) pres ON true
		 LEFT JOIN LATERAL (
		     SELECT last_telemetry_time(d.id) AS t
		 ) tele ON true
		 LEFT JOIN LATERAL (
		     SELECT GREATEST(pres.t, tele.t) AS at
		 ) last_activity ON true
		 WHERE d.id = $1 AND d.tenant_id = $2`+clause,
		append([]any{deviceID, tenantID}, args...)...).Scan(
		&device.ID, &device.CanonicalName, &profileID,
		&device.RuleChainGroupID, &device.CustomerID, &device.GroupID,
		&device.IsGateway, &device.GatewayID, &device.GatewayName,
		&device.FirmwareVersion,
		&device.State, &claimedAt, &decommissionedAt, &recallUntil,
		&quarantinedAt, &device.QuarantineReason,
		&createdAt, &updatedAt, &alias, &notes, &lastActivity,
		&device.ConnectorID, &device.ConnectorType, &device.ConnectorState,
		&device.GatewayConnectorCount, &device.AgentFeatures,
		&device.AgentOperations)
	if err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}
	if profileID != nil {
		device.ProfileID = *profileID
	}
	device.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	device.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	device.Alias = alias
	device.Notes = notes
	if lastActivity != nil {
		device.LastActivityAt = lastActivity.UTC().Format(time.RFC3339)
	}
	formatOptionalTime(claimedAt, &device.ClaimedAt)
	formatOptionalTime(decommissionedAt, &device.DecommissionedAt)
	formatOptionalTime(recallUntil, &device.RecallUntil)
	formatOptionalTime(quarantinedAt, &device.QuarantinedAt)
	return device, nil
}

// VisibleDeviceIDs returns the subset of the requested ids that are owned by
// the named tenant and visible under the data scope carried on ctx. It backs
// the WebSocket subscription path: a real-time subscription frame lists device
// ids, and the gateway must intersect that list with the caller's visible set
// before forwarding, rather than trusting the frame. When ids is empty it
// returns every visible id, which materializes a scoped member's wildcard
// subscription; an unrestricted scope returns nil with found=true (meaning
// "all" without enumerating the tenant's devices).
func (a *Admin) VisibleDeviceIDs(ctx context.Context, tenantID string, ids []string) (visible map[string]struct{}, all bool, err error) {
	scope := rbac.ScopeFromContext(ctx)
	if !scope.Restricted() {
		return nil, true, nil
	}
	visible = map[string]struct{}{}
	if len(ids) == 0 {
		clause, args := scope.DevicePredicate("d", 1)
		rows, qerr := a.pool.Query(ctx,
			`SELECT d.id::text FROM device d
			 WHERE d.tenant_id = $1`+clause,
			append([]any{tenantID}, args...)...)
		if qerr != nil {
			return nil, false, fmt.Errorf("list visible devices: %w", qerr)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				return nil, false, fmt.Errorf("scan visible device: %w", err)
			}
			visible[id] = struct{}{}
		}
		return visible, false, rows.Err()
	}

	// Compare as text: the ids come from a client frame and may be malformed,
	// and casting arbitrary strings to uuid[] would turn a bad value into a
	// query error instead of simply an "invisible" device.
	clause, args := scope.DevicePredicate("d", 2)
	rows, qerr := a.pool.Query(ctx,
		`SELECT d.id::text FROM device d
		 WHERE d.tenant_id = $1 AND d.id::text = ANY($2::text[])`+clause,
		append([]any{tenantID, ids}, args...)...)
	if qerr != nil {
		return nil, false, fmt.Errorf("filter visible devices: %w", qerr)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, false, fmt.Errorf("scan visible device: %w", err)
		}
		visible[id] = struct{}{}
	}
	return visible, false, rows.Err()
}

// ensureProfile verifies that a profile belongs to the tenant.
func ensureProfile(ctx context.Context, runner queryRunner, tenantID, profileID string) error {
	var id string
	err := runner.QueryRow(ctx,
		`SELECT id::text FROM device_profile WHERE id = $1 AND tenant_id = $2`,
		profileID, tenantID).Scan(&id)
	if err != nil {
		return mapSQLError(err)
	}
	return nil
}

// ensureChainGroup validates that a chain group binding references a group
// of the same tenant. An empty group ID is allowed and means the binding
// is unset (fall back to the next scope).
func ensureChainGroup(ctx context.Context, runner queryRunner, tenantID, groupID string) error {
	if groupID == "" {
		return nil
	}
	var id string
	err := runner.QueryRow(ctx,
		`SELECT id::text FROM rule_chain_group
		 WHERE id = $1 AND tenant_id = $2`,
		groupID, tenantID).Scan(&id)
	if err == pgx.ErrNoRows {
		return fmt.Errorf("%w: rule_chain_group_id does not reference a chain group of this tenant", ErrInvalidArgument)
	}
	if err != nil {
		return mapSQLError(err)
	}
	return nil
}

// insertDescriptor inserts or updates one descriptor. When createSpec is true
// (CreateProfile) every column is written from the DTO. On the update path
// writeEdgeSpec controls the vendor-owned spec columns
// (data_type/read_write/edge_default/edge_min/edge_max/inactive): only a
// console-authored manual profile may set them via the form. The point
// connector address is operator-maintainable on every source (an edge/import
// report commonly omits it), so it is always written. Neutral calibration
// columns (unit/transform/...) are always written. Ownership split is
// documented in ADR-0032 §5.
func insertDescriptor(ctx context.Context, tx pgx.Tx, tenantID string, descriptor *DescriptorDTO, profileID string, version int, createSpec, writeEdgeSpec bool) error {
	breakpoints, err := json.Marshal(descriptor.Breakpoints)
	if err != nil {
		return err
	}
	enumMap, err := json.Marshal(descriptor.EnumMap)
	if err != nil {
		return err
	}
	address := normalizeProfileTemplate(descriptor.Address, "{}")
	tolerance := descriptor.ToleranceMultiplier
	if tolerance < 1 {
		tolerance = defaultToleranceMultiplier
	}

	if createSpec || writeEdgeSpec {
		if _, err := tx.Exec(ctx,
			`INSERT INTO measurement_descriptor
			   (id, tenant_id, profile_id, measurement_key, unit, transform_type,
			    scale, offset_value, breakpoints, enum_map, expression, precision,
			    valid_min, valid_max, version,
			    data_type, read_write, address, edge_default, edge_min, edge_max, inactive,
			    expected_period_seconds, tolerance_multiplier)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
			         $16,$17,$18,$19,$20,$21,$22,$23,$24)
			 ON CONFLICT (id) DO UPDATE
			 SET measurement_key = EXCLUDED.measurement_key,
			     unit = EXCLUDED.unit,
			     transform_type = EXCLUDED.transform_type,
			     scale = EXCLUDED.scale,
			     offset_value = EXCLUDED.offset_value,
			     breakpoints = EXCLUDED.breakpoints,
			     enum_map = EXCLUDED.enum_map,
			     expression = EXCLUDED.expression,
			     precision = EXCLUDED.precision,
			     valid_min = EXCLUDED.valid_min,
			     valid_max = EXCLUDED.valid_max,
			     version = EXCLUDED.version,
			     data_type = EXCLUDED.data_type,
			     read_write = EXCLUDED.read_write,
			     address = EXCLUDED.address,
			     edge_default = EXCLUDED.edge_default,
			     edge_min = EXCLUDED.edge_min,
			     edge_max = EXCLUDED.edge_max,
			     inactive = EXCLUDED.inactive,
			     expected_period_seconds = EXCLUDED.expected_period_seconds,
			     tolerance_multiplier = EXCLUDED.tolerance_multiplier,
			     updated_at = now()`,
			descriptor.ID, tenantID, profileID, descriptor.MeasurementKey,
			descriptor.Unit, descriptor.TransformType, descriptor.Scale,
			descriptor.Offset, breakpoints, enumMap, descriptor.Expression,
			descriptor.Precision, descriptor.ValidMin, descriptor.ValidMax, version,
			descriptor.DataType, descriptor.ReadWrite, address,
			descriptor.EdgeDefault, descriptor.EdgeMin, descriptor.EdgeMax,
			descriptor.Inactive, descriptor.ExpectedPeriodSeconds, tolerance); err != nil {
			return mapSQLError(err)
		}
		return nil
	}

	// Edge/import update: persist the neutral calibration columns (including the
	// user-defined engineering unit), the always-writable connector address and
	// identity, but leave the remaining vendor-owned spec columns untouched.
	// syncEdgeDescriptors applies its own non-empty-only merge to unit on edge
	// replays; a console edit here is an explicit operator value and is stored.
	if _, err := tx.Exec(ctx,
		`INSERT INTO measurement_descriptor
		   (id, tenant_id, profile_id, measurement_key, unit, transform_type,
		    scale, offset_value, breakpoints, enum_map, expression, precision,
		    valid_min, valid_max, version, address,
		    expected_period_seconds, tolerance_multiplier)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		 ON CONFLICT (id) DO UPDATE
		 SET measurement_key = EXCLUDED.measurement_key,
		     unit = EXCLUDED.unit,
		     transform_type = EXCLUDED.transform_type,
		     scale = EXCLUDED.scale,
		     offset_value = EXCLUDED.offset_value,
		     breakpoints = EXCLUDED.breakpoints,
		     enum_map = EXCLUDED.enum_map,
		     expression = EXCLUDED.expression,
		     precision = EXCLUDED.precision,
		     valid_min = EXCLUDED.valid_min,
		     valid_max = EXCLUDED.valid_max,
		     version = EXCLUDED.version,
		     address = EXCLUDED.address,
		     expected_period_seconds = EXCLUDED.expected_period_seconds,
		     tolerance_multiplier = EXCLUDED.tolerance_multiplier,
		     updated_at = now()`,
		descriptor.ID, tenantID, profileID, descriptor.MeasurementKey,
		descriptor.Unit, descriptor.TransformType, descriptor.Scale,
		descriptor.Offset, breakpoints, enumMap, descriptor.Expression,
		descriptor.Precision, descriptor.ValidMin, descriptor.ValidMax, version,
		address, descriptor.ExpectedPeriodSeconds, tolerance); err != nil {
		return mapSQLError(err)
	}
	return nil
}

// defaultToleranceMultiplier is the silence deadline jitter multiplier used
// when an operator does not set one (ADR-0014).
const defaultToleranceMultiplier = 3.0

// descriptorChanged compares conversion-relevant fields.
func descriptorChanged(previous, current DescriptorDTO) bool {
	previous.Version = 0
	current.Version = 0
	previous.ID = ""
	current.ID = ""
	return !reflect.DeepEqual(previous, current)
}

// stripEdgeSpecColumns returns a copy of d with the vendor-owned spec
// columns blanked, so update diffs for edge/import profiles ignore fields the
// form cannot edit. The connector address is intentionally retained because
// it is operator-maintainable on every source. Unit is also retained: the
// platform never guesses engineering units and the user-defined descriptor is
// their sole source (ADR-0015 / hard rule), so the console may maintain it.
func stripEdgeSpecColumns(d DescriptorDTO) DescriptorDTO {
	d.DataType = ""
	d.ReadWrite = ""
	d.EdgeDefault = ""
	d.EdgeMin = nil
	d.EdgeMax = nil
	d.Inactive = false
	return d
}

// mapSQLError maps database errors to registry errors.
func mapSQLError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return fmt.Errorf("%w: %s", ErrConflict, pgErr.Message)
		case "23503":
			return fmt.Errorf("%w: %s", ErrInvalidArgument, pgErr.Message)
		}
	}
	return err
}
