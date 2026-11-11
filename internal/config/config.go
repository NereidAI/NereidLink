// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package config loads and validates the declarative platform configuration.
//
// The platform is configured from a single YAML file whose path is supplied
// through the global -c command line flag. Environment variables may override
// individual values in a later iteration; M0 only supports the file form.
package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration object consumed by the composition root.
type Config struct {
	// Server holds the user-facing HTTP/HTTPS listener settings.
	Server ServerSection `yaml:"server"`

	// Registry configures the in-cluster registry gRPC endpoint.
	Registry RegistrySection `yaml:"registry"`

	// Presence configures the ingress identity and cascading leases.
	Presence PresenceSection `yaml:"presence"`

	// Manifest configures the ADR-0026 gateway manifest reconciliation.
	Manifest ManifestSection `yaml:"manifest"`

	// LinkHealth configures the ADR-0026 gateway application watchdog.
	LinkHealth LinkHealthSection `yaml:"link_health"`

	// LinkNotify configures ADR-0026 link-down escalation and the daily
	// digest derived from watchdog advisories.
	LinkNotify LinkNotifySection `yaml:"link_notify"`

	// Connector configures the ADR-0026 edge connector desired/reported
	// lifecycle alarm sweeps.
	Connector ConnectorSection `yaml:"connector"`

	// AutoProvision configures the ADR-0026/ADR-0013 gateway
	// auto-provisioning of discovered local devices. It defaults to
	// disabled; an operator must turn it on explicitly.
	AutoProvision AutoProvisionSection `yaml:"auto_provision"`

	// NATS selects the messaging backbone endpoint.
	NATS NATSSection `yaml:"nats"`

	// Cluster enables multi-replica behavior (leader election, shared
	// OIDC state). Disabled for the single all-in-one process topology.
	Cluster ClusterSection `yaml:"cluster"`

	// Postgres selects the relational/timeseries database endpoint.
	Postgres PostgresSection `yaml:"postgres"`

	// IngestMQTT configures the southbound MQTT listener.
	IngestMQTT IngestMQTTSection `yaml:"ingest_mqtt"`

	// IngestHTTP configures the southbound HTTP device ingress.
	IngestHTTP IngestHTTPSection `yaml:"ingest_http"`

	// Telemetry tunes the JetStream-to-Timescale writer.
	Telemetry TelemetrySection `yaml:"telemetry"`

	// Command configures command routing, delivery and timeouts.
	Command CommandSection `yaml:"command"`

	// Probe configures on-demand gateway probe admission and retention.
	Probe ProbeSection `yaml:"probe"`

	// StorageHook configures the pre-storage hook pipeline, including the
	// wazero WASM hook sandbox budgets (ADR-0015).
	StorageHook StorageHookSection `yaml:"storage_hook"`

	// Blob configures the S3-compatible offload store for firmware, bulk
	// files and cold telemetry archives (ADR-0006). Empty endpoint disables
	// object storage; large payloads are then rejected rather than inlined.
	Blob BlobSection `yaml:"blob"`

	// Notify configures alarm notifications and built-in channels.
	Notify NotifySection `yaml:"notify"`

	// Deadman configures the independent platform self-check fallback
	// channel (ADR-0019), deliberately separate from Notify.
	Deadman DeadmanSection `yaml:"deadman"`

	// Accounting configures the ADR-0025 resource accounting sampler and
	// Top-N diagnostic API.
	Accounting AccountingSection `yaml:"accounting"`

	// Rules configures the rule processing consumers.
	Rules RulesSection `yaml:"rules"`

	// Seed configures the deterministic development seed data.
	Seed SeedSection `yaml:"seed"`

	// Module configures the southbound add-on registry leases.
	Module ModuleSection `yaml:"module"`

	// Provision tunes the tenant provisioning reconciler.
	Provision ProvisionSection `yaml:"provision"`

	// Identity configures local authentication.
	Identity IdentitySection `yaml:"identity"`

	// OIDC configures external OpenID Connect single sign-on.
	OIDC OIDCSection `yaml:"oidc"`
	SSO  SSOSection  `yaml:"sso"`

	// License configures offline license verification and metering.
	License LicenseSection `yaml:"license"`

	// OpsAdmin configures the physically separated ops listener.
	OpsAdmin OpsAdminSection `yaml:"ops_admin"`

	// Bus configures internal message-bus compression.
	Bus BusSection `yaml:"bus"`

	// IngestLimit configures per-device ingest rate limiting.
	IngestLimit IngestLimitSection `yaml:"ingest_limit"`

	// SilenceDetection configures the online-but-silent sweep (ADR-0014).
	SilenceDetection SilenceDetectionSection `yaml:"silence_detection"`

	// Retention configures automatic data-retention enforcement.
	Retention RetentionSection `yaml:"retention"`

	// CredentialCache configures the optional L1/L2 cache in front of
	// device credential authentication (ADR-0014). It is disabled by
	// default: all-in-one and small deployments keep authenticating
	// directly against PostgreSQL, and when disabled every other field is
	// ignored.
	CredentialCache CredentialCacheSection `yaml:"credential_cache"`

	// Horizon configures the NereidHorizon model-distribution integration
	// (nh-model-distribution/v1). Disabled by default; when disabled the
	// model_distribution preflight blocks instead of silently succeeding.
	Horizon HorizonSection `yaml:"horizon"`
}

// CredentialCacheSection configures the optional credential cache. The
// master switch defaults to false so deployments that name no section keep
// querying PostgreSQL on every new connection.
type CredentialCacheSection struct {
	// Enabled is the master switch; defaults to false.
	Enabled bool `yaml:"enabled"`

	// PositiveTTL bounds how long a resolved credential record is reused;
	// empty uses the built-in default. The effective TTL is additionally
	// capped at the credential expiry so an expiring credential cannot be
	// served past its window.
	PositiveTTL string `yaml:"positive_ttl"`

	// NegativeTTL bounds caching of failed and unknown credentials; empty
	// uses the built-in default.
	NegativeTTL string `yaml:"negative_ttl"`

	// MaxCost is the L1 cost budget in approximate bytes; 0 uses the
	// built-in default.
	MaxCost int64 `yaml:"max_cost"`

	// NumCounters is the ristretto admission counter count; 0 uses the
	// built-in default.
	NumCounters int64 `yaml:"num_counters"`

	// L2 configures the shared NATS KV tier.
	L2 CredentialCacheL2Section `yaml:"l2"`
}

// CredentialCacheL2Section configures the shared NATS KV tier. L2 follows
// the master switch: when credential_cache.enabled is true it is on unless
// explicitly disabled.
type CredentialCacheL2Section struct {
	// Disabled marks an L1-only node. Default false keeps the L2 tier on.
	Disabled bool `yaml:"disabled"`

	// Bucket is the NATS KV bucket name; empty uses CREDENTIALS.
	Bucket string `yaml:"bucket"`
}

// RetentionSection configures the periodic retention enforcer (ADR-0005).
type RetentionSection struct {
	// SweepInterval paces the automatic expiry pass.
	SweepInterval string `yaml:"sweep_interval"`
}

// IngestLimitSection configures the in-process per-device token bucket
// (ADR-0014).
type IngestLimitSection struct {
	// PerDeviceRate is steady-state frames per second per device;
	// 0 or negative means unlimited.
	PerDeviceRate float64 `yaml:"per_device_rate"`

	// PerDeviceBurst is the bucket capacity; 0 derives one second of rate.
	PerDeviceBurst float64 `yaml:"per_device_burst"`

	// SweepInterval paces idle-bucket eviction as a duration string.
	SweepInterval string `yaml:"sweep_interval"`

	// Idle bounds how long an unused bucket is kept.
	Idle string `yaml:"idle"`

	// FloodThreshold opens a device flood alarm after this many rejections
	// within FloodWindow; 0 disables flood detection (ADR-0014).
	FloodThreshold int `yaml:"flood_threshold"`

	// FloodWindow is the sliding window over which rejections are counted.
	FloodWindow string `yaml:"flood_window"`

	// FloodRecover is the calm interval after which an open flood clears.
	FloodRecover string `yaml:"flood_recover"`
}

// SilenceDetectionSection configures the ADR-0014 online-but-silent sweep.
// Per-device reporting periods live on measurement descriptors; this only
// turns the periodic reconciler on and paces it.
type SilenceDetectionSection struct {
	// Enabled turns the periodic sweep on.
	Enabled bool `yaml:"enabled"`

	// SweepInterval paces reconciliation as a duration string.
	SweepInterval string `yaml:"sweep_interval"`
}

// BusSection configures application-layer bus compression (ADR-0003).
type BusSection struct {
	// Compression selects off, fast, balanced or max.
	Compression CompressionSection `yaml:"compression"`
}

// CompressionSection configures the bus compression codec.
type CompressionSection struct {
	// Level is off (default), fast, balanced or max.
	Level string `yaml:"level"`

	// MinBytes skips messages smaller than this; 0 uses the codec default.
	MinBytes int `yaml:"min_bytes"`
}

// OpsAdminSection configures the ADR-0023 ops administration
// listener. It is intentionally separate from the business API and
// should bind to loopback or a management-only network.
type OpsAdminSection struct {
	// Listen is the bind address; empty disables the listener.
	Listen string `yaml:"listen"`

	// ShutdownTimeout bounds graceful shutdown as a duration string.
	ShutdownTimeout string `yaml:"shutdown_timeout"`
}

// IdentitySection configures the local identity fallback.
type IdentitySection struct {
	// JWTSecret signs access tokens (HS256); at least 16 characters.
	JWTSecret string `yaml:"jwt_secret"`

	// AccessTTL is the access-token lifetime as a duration string.
	AccessTTL string `yaml:"access_ttl"`

	// RefreshTTL is the refresh-session lifetime as a duration string.
	RefreshTTL string `yaml:"refresh_ttl"`
}

// OIDCSection configures external OpenID Connect single sign-on (ADR-0021).
// A zero/empty Issuer disables OIDC; the login page then offers local
// credentials only.
type OIDCSection struct {
	// Enabled gates the SSO flow independent of the other fields.
	Enabled bool `yaml:"enabled"`

	// Issuer is the OIDC issuer base URL whose /.well-known/openid-configuration
	// document is used for discovery.
	Issuer string `yaml:"issuer"`

	// ClientID is the Relying Party client id registered at the IdP.
	ClientID string `yaml:"client_id"`

	// ClientSecret is the Relying Party client secret (confidential client).
	ClientSecret string `yaml:"client_secret"`

	// RedirectURL must exactly match the registered IdP redirect URI.
	RedirectURL string `yaml:"redirect_url"`

	// FrontendBaseURL is where the backend redirects the browser after the
	// IdP callback (the SPA /auth/callback route). Empty falls back to the
	// request origin's root.
	FrontendBaseURL string `yaml:"frontend_base_url"`

	// Scopes overrides the default openid/email/profile list.
	Scopes []string `yaml:"scopes"`

	// TenantID is the tenant JIT-provisioned users and group-mapped roles are
	// attached to.
	TenantID string `yaml:"tenant_id"`

	// DefaultRole is assigned to a provisioned user when no group claim maps.
	DefaultRole string `yaml:"default_role"`

	// GroupsClaim names the token claim carrying IdP group names.
	GroupsClaim string `yaml:"groups_claim"`

	// GroupRole maps an IdP group name to a built-in role.
	GroupRole map[string]string `yaml:"group_role"`

	// StateTTL bounds the authorization-code state/nonce/PKCE cookie store.
	StateTTL string `yaml:"state_ttl"`

	// StateStore selects where the single-use authorization state lives:
	// "memory" (default, single process) or "nats" (shared KV, required
	// when multiple serve replicas run behind one ingress).
	StateStore string `yaml:"state_store"`

	// CAFile is an optional PEM bundle trusted in addition to the system
	// roots for discovery, JWKS and token exchange. Use it when the IdP
	// presents an internal/private CA certificate.
	CAFile string `yaml:"ca_file"`
}

// SSOSection configures NereidSphere (NS) one-time handoff ticket mode.
// In this mode the suite portal (NS) mints a 30-second single-use ticket
// after the user logs in, redirects the browser to the product's callback
// endpoint with the ticket, and the product exchanges it server-to-server
// at NS's /api/v1/auth/ticket/verify using its shared secret. This avoids
// the product needing its own OIDC client registration and keeps the
// identity plane entirely in NS.
type SSOSection struct {
	// Enabled gates the NS ticket flow. When true, oidc.enabled must be
	// false (mutually exclusive SSO modes).
	Enabled bool `yaml:"enabled"`

	// NSBaseURL is the public origin of NereidSphere (e.g.
	// https://nereidsphere.example.com). Used for the ticket verification
	// endpoint and to build the NS login URL for unauthenticated visitors.
	NSBaseURL string `yaml:"ns_base_url"`

	// ClientID is the product identifier as registered in NS's products
	// list (e.g. "nereidlink"). Must match the NS product's ID and SSOSecret.
	ClientID string `yaml:"client_id"`

	// SSOSecret is the shared secret for server-to-server ticket exchange.
	// Rendered from the deployment Secret alongside the DSN and OIDC secret.
	SSOSecret string `yaml:"sso_secret"`

	// CallbackPath is the product endpoint that receives the browser
	// redirect carrying the ticket (default "/auth/ns-callback"). It must
	// equal the product's sso_callback_path registered in NS.
	CallbackPath string `yaml:"callback_path"`

	// FrontendBaseURL is where the backend redirects the browser after
	// successful ticket verification (the SPA /auth/callback route). Empty
	// falls back to the request origin's root.
	FrontendBaseURL string `yaml:"frontend_base_url"`

	// TenantID is the tenant JIT-provisioned users and group-mapped roles
	// are attached to. Defaults to the seed tenant.
	TenantID string `yaml:"tenant_id"`

	// DefaultRole is assigned to a provisioned user when no ticket role
	// maps. Allowed: TENANT_ADMIN, ENGINEER, CUSTOMER_USER, READ_ONLY.
	DefaultRole string `yaml:"default_role"`

	// RoleMap maps an NS identity role (Keycloak realm role carried in the
	// ticket's identity.roles) to a built-in role, e.g.
	// {"admin": "TENANT_ADMIN"}. Used on the no-groups fallback path.
	RoleMap map[string]string `yaml:"role_map"`

	// GroupRole maps the last path segment of a
	// /nereidlink/{tenant}/{role} group to a built-in NL role. Defaults to
	// admins→TENANT_ADMIN, engineers→ENGINEER, read_only→READ_ONLY.
	GroupRole map[string]string `yaml:"group_role"`

	// ReconcileMemberships revokes suite-managed membership roles on each
	// group-based SSO login when the assignment was removed upstream.
	// Defaults to true.
	ReconcileMemberships *bool `yaml:"reconcile_memberships"`

	// CAFile is an optional PEM bundle trusted in addition to the system
	// roots for the NS ticket exchange HTTPS call. Use it when NS presents
	// an internal/private CA certificate.
	CAFile string `yaml:"ca_file"`
}

// LicenseSection configures offline signed licenses.
type LicenseSection struct {
	// PublicKeyPath is the vendor Ed25519 PEM public key.
	PublicKeyPath string `yaml:"public_key_path"`

	// GracePeriod is the warning/action window as a duration string.
	GracePeriod string `yaml:"grace_period"`

	// ClockFile is the local 0600 rollback witness path.
	ClockFile string `yaml:"clock_file"`
}

// ProvisionSection configures the tenant provisioning reconciler.
type ProvisionSection struct {
	// PollInterval is how often the reconciler scans for due tasks.
	PollInterval string `yaml:"poll_interval"`

	// Lease is the claim lease as a duration string; it auto-expires
	// when a worker dies mid-run.
	Lease string `yaml:"lease"`

	// MaxAttempts stops retry after this many failures; 0 = unlimited.
	MaxAttempts int `yaml:"max_attempts"`

	// MinBackoff is the first retry delay.
	MinBackoff string `yaml:"min_backoff"`

	// MaxBackoff caps exponential backoff.
	MaxBackoff string `yaml:"max_backoff"`
}

// ServerSection configures gateway-api.
type ServerSection struct {
	// HTTPListen is the bind address for REST and WebSocket traffic.
	HTTPListen string `yaml:"http_listen"`

	// WSPath is the path at which the WebSocket endpoint is exposed.
	WSPath string `yaml:"ws_path"`

	// WSReadLimit caps inbound WebSocket control frame sizes in bytes.
	WSReadLimit int64 `yaml:"ws_read_limit"`

	// WSPingInterval is the application ping interval as a duration string.
	WSPingInterval string `yaml:"ws_ping_interval"`

	// ShutdownTimeout bounds graceful HTTP shutdown as a duration string.
	ShutdownTimeout string `yaml:"shutdown_timeout"`
}

// PresenceSection configures cascading presence leases.
type PresenceSection struct {
	// IngressID is the stable identity of this node in topology.
	IngressID string `yaml:"ingress_id"`

	// HeartbeatInterval refreshes the node lease as a duration string.
	HeartbeatInterval string `yaml:"heartbeat_interval"`

	// LeaseTTL is the node lease time-to-live as a duration string.
	LeaseTTL string `yaml:"lease_ttl"`

	// OfflineBatchSize bounds derived offline batches.
	OfflineBatchSize int `yaml:"offline_batch_size"`

	// OfflineBatchInterval paces derived offline deletion as a duration.
	OfflineBatchInterval string `yaml:"offline_batch_interval"`

	// PersistBufferSize bounds the presence history queue.
	PersistBufferSize int `yaml:"persist_buffer_size"`
}

// ManifestSection configures gateway manifest reconciliation (ADR-0026).
type ManifestSection struct {
	// LeaseTTL bounds how long a gateway's last manifest keeps its lease
	// alive before attached sub-devices are derived offline, as a duration
	// string. It should span several manifest intervals (the edge sends on
	// 5-minute wall-clock boundaries).
	LeaseTTL string `yaml:"lease_ttl"`

	// SweepInterval paces the global sweep that backstops silent KV TTL
	// expiry, as a duration string.
	SweepInterval string `yaml:"sweep_interval"`
}

// LinkHealthSection configures the gateway application watchdog (ADR-0026):
// application heartbeats, SUSPECT/DOWN lease thresholds and flapping.
type LinkHealthSection struct {
	// HeartbeatInterval is the expected edge heartbeat period.
	HeartbeatInterval string `yaml:"heartbeat_interval"`

	// SuspectAfter is the silence before the first missed beat declares
	// SUSPECT, as a duration string.
	SuspectAfter string `yaml:"suspect_after"`

	// DownAfter is the total silence from the last heartbeat that declares
	// DOWN, as a duration string.
	DownAfter string `yaml:"down_after"`

	// SweepInterval paces the global link evaluation, as a duration string.
	SweepInterval string `yaml:"sweep_interval"`

	// FlapWindow bounds the sliding window used for flapping detection.
	FlapWindow string `yaml:"flap_window"`

	// FlapThreshold is the number of unplanned down episodes in FlapWindow
	// that raises a flapping advisory. Zero selects the built-in default.
	FlapThreshold int `yaml:"flap_threshold"`
}

// LinkNotifySection configures gateway link-down escalation and the daily
// digest (ADR-0026).
type LinkNotifySection struct {
	// EscalationAfter bounds how long an outage stays MAJOR before it is
	// bumped once to CRITICAL, as a duration string.
	EscalationAfter string `yaml:"escalation_after"`

	// EscalationSweepInterval paces the open-episode scan.
	EscalationSweepInterval string `yaml:"escalation_sweep_interval"`

	// DigestEnabled turns the 24-hour summary on. Empty means default (on).
	DigestEnabled *bool `yaml:"digest_enabled"`

	// DigestHourUTC is the UTC hour (0-23) the previous-day digest is sent.
	// Zero selects the built-in default.
	DigestHourUTC int `yaml:"digest_hour_utc"`

	// DigestSweepInterval paces the clock check for the daily send.
	DigestSweepInterval string `yaml:"digest_sweep_interval"`
}

// ConnectorSection configures the edge connector desired/reported lifecycle
// alarm sweeps (ADR-0026).
type ConnectorSection struct {
	// PendingAfter bounds how long a fresh desired revision may stay
	// without an APPLIED report before a WARNING alarm is raised.
	PendingAfter string `yaml:"pending_after"`

	// FailedEscalationAfter bounds how long a continuously FAILED
	// connector stays MAJOR before it is bumped once to CRITICAL.
	FailedEscalationAfter string `yaml:"failed_escalation_after"`

	// SweepInterval paces the pending/failed alarm scans.
	SweepInterval string `yaml:"sweep_interval"`

	// StrictReconcile makes desired win when a gateway manifest shows the
	// edge running missing, stale or content-mutated connector instances:
	// NL re-pushes desired and deletes unrecognized instances. It defaults
	// to false, in which case drift only raises an observability alarm.
	StrictReconcile bool `yaml:"strict_reconcile"`
}

// AutoProvisionSection configures gateway auto-provisioning from the edge
// discovery uplink (ADR-0026/ADR-0013).
type AutoProvisionSection struct {
	// Enabled gates the whole feature. It defaults to false: an operator
	// must opt in before unregistered local devices are created.
	Enabled bool `yaml:"enabled"`

	// MaxPerCycle bounds how many NEW devices one discovery frame may
	// insert. Already-existing name matches do not count.
	MaxPerCycle int `yaml:"max_per_cycle"`

	// MaxCandidates bounds the number of candidates accepted in one frame.
	MaxCandidates int `yaml:"max_candidates"`

	// DefaultProfile is a tenant profile UUID or name applied to candidates
	// that do not name their own profile. Empty creates profile-less
	// devices, which is only sensible when descriptors come from elsewhere.
	DefaultProfile string `yaml:"default_profile"`

	// ApprovalMode selects immediate creation ("auto", default) or a human
	// review queue ("review", migration 0046). Per-tenant/gateway policy
	// overrides still win over this deployment fallback.
	ApprovalMode string `yaml:"approval_mode"`
}

// RegistrySection configures the registry gRPC service.
type RegistrySection struct {
	// Listen is the bind address used by the in-process server and dialed
	// by co-located ingress roles.
	Listen string `yaml:"listen"`

	// ModuleToken, when set, requires southbound module clients to present
	// it as a bearer token. Empty keeps the loopback endpoint open for the
	// embedded ingresses.
	ModuleToken string `yaml:"module_token"`

	// TLS hardens the registry gRPC link (ADR-0003). When enabled the
	// server presents CertFile/KeyFile and the co-located client verifies
	// the server against CAFile; setting CAFile also mTLS-requires client
	// certificates.
	TLS LinkTLSSection `yaml:"tls"`
}

// ModuleSection configures southbound module heartbeat leases.
type ModuleSection struct {
	// HeartbeatLease bounds how long after the last heartbeat a module
	// stays ONLINE.
	HeartbeatLease string `yaml:"heartbeat_lease"`

	// SweepInterval paces the stale-lease sweeper.
	SweepInterval string `yaml:"sweep_interval"`
}

// NATSSection configures the connection to NATS.
type NATSSection struct {
	// URL is the NATS client URL, e.g. nats://127.0.0.1:4222.
	URL string `yaml:"url"`

	// JetStreamReplicas is the Raft replication factor for every stream and
	// KV bucket the platform creates. 1 (the default) targets the bundled
	// single-node topologies (docker compose, one-pod k3s); clustered NATS
	// deployments use 3 (or 5). JetStream allows only odd counts <= 5.
	JetStreamReplicas int `yaml:"jetstream_replicas"`

	// TLS configures TLS 1.3 for the internal NATS link (ADR-0003, section
	// "internal link encryption"). When disabled the client uses plaintext.
	TLS LinkTLSSection `yaml:"tls"`
}

// ClusterSection controls multi-replica behavior. It defaults to a single
// all-in-one process: background workers run directly, OIDC login state is
// kept in memory and JetStream resources use one replica. Enabling it (the
// Helm HA values) switches periodic workers to NATS-KV leader election,
// OIDC state to a shared KV bucket and requires an unique instance_id per
// pod.
type ClusterSection struct {
	// Enabled turns on leader-elected workers and shared OIDC state.
	Enabled bool `yaml:"enabled"`

	// InstanceID identifies this process in a replica set; empty defaults
	// to the host name. It must be unique per pod.
	InstanceID string `yaml:"instance_id"`

	// LeaseTTL bounds how long a dead leader's lease can block failover.
	LeaseTTL string `yaml:"lease_ttl"`
}

// LinkTLSSection configures TLS for an internal (node-to-node) link. It is
// shared by the NATS client and any other internal gRPC/NATS transport so
// the platform enforces one hardened profile: TLS 1.3 only, with an
// optional client certificate for mutual authentication.
type LinkTLSSection struct {
	// Enabled switches the link to TLS; when false the connection is
	// plaintext (development only).
	Enabled bool `yaml:"enabled"`

	// CAFile is the PEM bundle used to verify the peer certificate. Empty
	// falls back to the system roots.
	CAFile string `yaml:"ca_file"`

	// CertFile and KeyFile are the client certificate/key presented for
	// mutual TLS. Both empty disables client authentication.
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`

	// InsecureSkipVerify disables peer verification and must only be used
	// for local testing; loading rejects it in combination with TLS.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`
}

// PostgresSection configures the connection to PostgreSQL/TimescaleDB.
type PostgresSection struct {
	// DSN is the PostgreSQL connection string.
	DSN string `yaml:"dsn"`
}

// IngestMQTTSection configures the embedded MQTT broker.
type IngestMQTTSection struct {
	// Listen is the TCP bind address of the plain MQTT listener.
	Listen string `yaml:"listen"`

	// TLSListen is the mTLS bind address; empty disables the TLS listener.
	TLSListen string `yaml:"tls_listen"`

	// TLSCertFile is the server certificate PEM for the TLS listener.
	TLSCertFile string `yaml:"tls_cert_file"`

	// TLSKeyFile is the server private key PEM for the TLS listener.
	TLSKeyFile string `yaml:"tls_key_file"`

	// TLSClientCAFile is the PEM of the CA trusted for device certificates.
	TLSClientCAFile string `yaml:"tls_client_ca_file"`

	// TLSRequireClientCert switches the TLS listener from optional client
	// certificates (MQTTS with username/password) to strict mTLS.
	TLSRequireClientCert bool `yaml:"tls_require_client_cert"`
}

// IngestHTTPSection configures the southbound HTTP device ingress, which
// is served on the management HTTP listener under /v1/me.
type IngestHTTPSection struct {
	// IngressID is the dispatch identity of the HTTP ingress, distinct
	// from the MQTT ingress so commands route to the HTTP relay.
	IngressID string `yaml:"ingress_id"`

	// LongPollWait bounds one GET commands request as a duration string.
	LongPollWait string `yaml:"long_poll_wait"`

	// SessionTTL bounds the presence session idle time as a duration string.
	SessionTTL string `yaml:"session_ttl"`

	// SweepInterval paces the idle session sweeper as a duration string.
	SweepInterval string `yaml:"sweep_interval"`

	// MailboxSize bounds buffered commands of one device.
	MailboxSize int `yaml:"mailbox_size"`

	// MaxTelemetryBytes bounds an accepted telemetry request body.
	MaxTelemetryBytes int64 `yaml:"max_telemetry_bytes"`
}

// TelemetrySection tunes the telemetry writer batching behavior.
type TelemetrySection struct {
	// FlushInterval is the maximum wait before a buffered batch is written.
	FlushInterval string `yaml:"flush_interval"`

	// FlushSize is the row count at which a batch flushes immediately.
	FlushSize int `yaml:"flush_size"`

	// MaxAckPending bounds in-flight messages per shard consumer.
	MaxAckPending int `yaml:"max_ack_pending"`

	// DedupWindow bounds the envelope redelivery cache size.
	DedupWindow int `yaml:"dedup_window"`
}

// CommandSection configures command routing, delivery and timeouts.
type CommandSection struct {
	// DefaultTimeout is used when a catalog entry declares no timeout.
	DefaultTimeout string `yaml:"default_timeout"`

	// PersistentTTL bounds how long undelivered persistent commands wait.
	PersistentTTL string `yaml:"persistent_ttl"`

	// DeliveryTimeout bounds the transport-level delivery ack wait.
	DeliveryTimeout string `yaml:"delivery_timeout"`

	// SweepInterval is the expiry sweep interval as a duration string.
	SweepInterval string `yaml:"sweep_interval"`

	// ClaimBatchSize bounds commands claimed per reconnect event.
	ClaimBatchSize int `yaml:"claim_batch_size"`

	// MaxBodyBytes bounds the command parameter document size.
	MaxBodyBytes int `yaml:"max_body_bytes"`

	// MaxBatchTargets bounds the number of devices in one batch task.
	MaxBatchTargets int `yaml:"max_batch_targets"`
}

// ProbeSection configures on-demand gateway probing (ADR-0026 section 9).
// Zero/empty fields fall back to the documented ADR defaults.
type ProbeSection struct {
	// MaxDevices bounds the device count accepted in one task (default 100).
	MaxDevices int `yaml:"max_devices"`

	// CreateRate is the minimum accepted interval between task creations
	// for one principal (default 1s, i.e. 1 QPS).
	CreateRate string `yaml:"create_rate"`

	// MaxConcurrent bounds unfinished tasks per principal (default 5).
	MaxConcurrent int `yaml:"max_concurrent"`

	// Cooldown is the minimum interval between dispatches touching the same
	// device (default 10s).
	Cooldown string `yaml:"cooldown"`

	// DefaultTimeout is the per-device probe hint when the request omits one
	// (default 10s).
	DefaultTimeout string `yaml:"default_timeout"`

	// TaskDeadline bounds how long a task waits for outstanding responses
	// (default 60s).
	TaskDeadline string `yaml:"task_deadline"`

	// Retention is how long finished task results are kept (default 24h).
	Retention string `yaml:"retention"`

	// SweepInterval paces retention/deadline sweeps (default 1m).
	SweepInterval string `yaml:"sweep_interval"`
}

// StorageHookSection configures the pre-storage hook pipeline and the wazero
// WASM hook sandbox (ADR-0015). Zero/empty fields fall back to the documented
// defaults; the sandbox is enabled by default.
type StorageHookSection struct {
	// WasmEnabled toggles wazero-backed wasm hooks (default true).
	WasmEnabled *bool `yaml:"wasm_enabled"`

	// WasmMaxModuleBytes bounds an uploaded module, decoded size
	// (default 2 MiB).
	WasmMaxModuleBytes int `yaml:"wasm_max_module_bytes"`

	// WasmMemoryPages caps one invocation's linear memory in 64 KiB
	// WebAssembly pages (default 256 = 16 MiB).
	WasmMemoryPages uint32 `yaml:"wasm_memory_pages"`

	// WasmMaxOutputBytes bounds the stdout JSON overlay a module may emit
	// (default 64 KiB).
	WasmMaxOutputBytes int `yaml:"wasm_max_output_bytes"`
}

// BlobSection configures binary-object storage (ADR-0006). Two backends
// exist: an S3-compatible object store and a local directory intended for
// single-node all-in-one deployments. Provider selects between them; "auto"
// uses S3 when its fields are complete and otherwise falls back to the local
// directory, so an all-in-one node serves binaries with zero extra
// infrastructure.
type BlobSection struct {
	// Provider is "auto" (default), "s3" or "local".
	Provider string `yaml:"provider"`

	// Endpoint is the internal host[:port] for put/list/delete.
	Endpoint string `yaml:"endpoint"`
	// PublicEndpoint is the host[:port] embedded in presigned download URLs
	// (what gateways reach); defaults to Endpoint.
	PublicEndpoint string `yaml:"public_endpoint"`
	Region         string `yaml:"region"`
	AccessKey      string `yaml:"access_key"`
	SecretKey      string `yaml:"secret_key"`
	Bucket         string `yaml:"bucket"`
	// PresignTTL is the presigned download URL lifetime (default 15m).
	PresignTTL string `yaml:"presign_ttl"`
	// Secure selects HTTPS for the internal endpoint.
	Secure bool `yaml:"secure"`
	// PublicSecure selects HTTPS for presigned download URLs; the
	// in-cluster endpoint is often plain HTTP while the public route is
	// TLS-terminated by an ingress. Defaults to false (http).
	PublicSecure bool `yaml:"public_secure"`
	// MaxUploadBytes caps a single platform upload (default 512 MiB).
	MaxUploadBytes int64 `yaml:"max_upload_bytes"`

	// MediaMaxBytes caps one device media object, e.g. a camera image
	// (default 10 MiB).
	MediaMaxBytes int64 `yaml:"media_max_bytes"`
	// MediaAllowedTypes restricts accepted media content types; an entry
	// ending in "/" is a prefix (image/), others match exactly. Empty
	// defaults to image/.
	MediaAllowedTypes []string `yaml:"media_allowed_types"`

	// Local configures the directory backend.
	Local BlobLocalSection `yaml:"local"`
}

// BlobLocalSection configures the filesystem backend. The platform streams
// objects as regular files and serves downloads (including HTTP Range)
// through its own HTTP listener.
type BlobLocalSection struct {
	// Directory holds the objects; empty uses ./blobs.
	Directory string `yaml:"directory"`
}

// Blob provider identifiers.
const (
	// BlobProviderAuto selects S3 when fully configured, else local.
	BlobProviderAuto = "auto"
	// BlobProviderS3 forces the S3 backend.
	BlobProviderS3 = "s3"
	// BlobProviderLocal forces the directory backend.
	BlobProviderLocal = "local"
)

// DefaultBlobLocalDirectory is used when the local backend is selected and
// no directory is named.
const DefaultBlobLocalDirectory = "./blobs"

// S3Configured reports whether every required S3 field is present.
func (b BlobSection) S3Configured() bool {
	return b.Endpoint != "" && b.AccessKey != "" && b.SecretKey != "" && b.Bucket != ""
}

// Backend resolves Provider to the effective backend: BlobProviderS3,
// BlobProviderLocal or "" (disabled). An explicit "s3" with incomplete S3
// settings is disabled rather than silently falling back, surfacing the
// configuration error; "auto" falls back to local.
func (b BlobSection) Backend() string {
	switch b.Provider {
	case "", BlobProviderAuto:
		if b.S3Configured() {
			return BlobProviderS3
		}
		return BlobProviderLocal
	case BlobProviderS3:
		if b.S3Configured() {
			return BlobProviderS3
		}
		return ""
	case BlobProviderLocal:
		return BlobProviderLocal
	default:
		return ""
	}
}

// LocalDirectory returns the effective directory for the local backend.
func (b BlobSection) LocalDirectory() string {
	if b.Local.Directory != "" {
		return b.Local.Directory
	}
	return DefaultBlobLocalDirectory
}

// Enabled reports whether any blob backend is effective. Kept for callers
// that only need the on/off answer.
func (b BlobSection) Enabled() bool {
	return b.Backend() != ""
}

// HorizonSection configures the NereidHorizon model-distribution integration
// (contract nh-model-distribution/v1). It is platform kernel infrastructure
// like NATS or PostgreSQL, not tenant business data, so it lives in the
// deployment config. Disabled by default: model_distribution preflight then
// blocks with model_distribution_unavailable instead of silently skipping
// distribution.
type HorizonSection struct {
	// Enabled switches the NH ensure client on.
	Enabled bool `yaml:"enabled"`
	// BaseURL is the NereidHorizon BFF base URL (no trailing slash needed).
	BaseURL string `yaml:"base_url"`
	// Org is the NH organization the API key is bound to. It is inserted
	// into every /api/service/v1/orgs/{org}/... path.
	Org string `yaml:"org"`
	// APIKey is the org-bound NH service key carrying model:distribute.
	// Supply it through a Secret config overlay, never a committed profile.
	APIKey string `yaml:"api_key"`
	// DefaultPrecision selects the Forge precision package when a model
	// assembly node does not declare one. Empty means fp16.
	DefaultPrecision string `yaml:"default_precision"`
	// GatewayDestinations maps an NL gateway ID to its ESS destination token
	// ("<destinationType>:<destinationID>", e.g. "openhorizon.edgenode:node1").
	// A model artifact whose gateway is unmapped is a blocking preflight
	// finding, never a guessed destination.
	GatewayDestinations map[string]string `yaml:"gateway_destinations"`
	// CAFile is an optional PEM bundle for verifying the NH certificate.
	CAFile string `yaml:"ca_file"`
	// InsecureSkipVerify disables NH certificate verification and is only
	// valid for local lab testing; Load rejects it together with a CA file.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`
}

// Horizon precision identifiers, mirroring the NH contract's closed enum.
const (
	HorizonPrecisionFP16 = "fp16"
	HorizonPrecisionINT8 = "int8"
)

// PrecisionOrDefault returns the validated explicit precision or the fp16
// default. An invalid value is reported to the caller rather than silently
// rewritten.
func (h HorizonSection) PrecisionOrDefault(explicit string) (string, error) {
	value := explicit
	if value == "" {
		value = h.DefaultPrecision
	}
	if value == "" {
		return HorizonPrecisionFP16, nil
	}
	switch value {
	case HorizonPrecisionFP16, HorizonPrecisionINT8:
		return value, nil
	default:
		return "", fmt.Errorf("invalid horizon precision %q: must be fp16 or int8", value)
	}
}

// validateHorizon mirrors the NH envelope rules locally so a misconfigured
// deployment fails at startup instead of on the first model deployment.
func (c *Config) validateHorizon() error {
	h := c.Horizon
	if !h.Enabled {
		return nil
	}
	if strings.TrimSpace(h.BaseURL) == "" {
		return fmt.Errorf("horizon.enabled requires horizon.base_url")
	}
	if strings.TrimSpace(h.Org) == "" || h.Org == "root" {
		return fmt.Errorf("horizon.enabled requires a non-root horizon.org")
	}
	if strings.TrimSpace(h.APIKey) == "" {
		return fmt.Errorf("horizon.enabled requires horizon.api_key")
	}
	if h.InsecureSkipVerify && h.CAFile != "" {
		return fmt.Errorf("horizon.insecure_skip_verify cannot be combined with horizon.ca_file")
	}
	if _, err := h.PrecisionOrDefault(""); err != nil {
		return fmt.Errorf("horizon.default_precision: %w", err)
	}
	for gateway, destination := range h.GatewayDestinations {
		if strings.TrimSpace(gateway) == "" {
			return fmt.Errorf("horizon.gateway_destinations has an empty gateway id")
		}
		if !strings.Contains(destination, ":") || strings.ContainsAny(destination, "<>") {
			return fmt.Errorf("horizon gateway %s maps to an invalid ESS destination token %q", gateway, destination)
		}
	}
	return nil
}

// NotifySMTPSection is the notify SMTP subsection.
type NotifySMTPSection struct {
	Enabled  bool   `yaml:"enabled"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	From     string `yaml:"from"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// NotifySection configures the notification service.
type NotifySection struct {
	FrequencyWindow string            `yaml:"frequency_window"`
	WebhookURL      string            `yaml:"webhook_url"`
	SMTP            NotifySMTPSection `yaml:"smtp"`
	CriticalWorkers int               `yaml:"critical_workers"`
	Workers         int               `yaml:"workers"`
	DeliveryTimeout string            `yaml:"delivery_timeout"`
	EmailRecipients []string          `yaml:"email_recipients"`
}

// DeadmanSection configures the ADR-0019 independent fallback channel used
// for the platform's own CRITICAL problems. Its endpoint must be separate
// from the tenant notifier so a failure there cannot hide a platform
// outage.
type DeadmanSection struct {
	// Enabled turns the platform self-check and its reporting on.
	Enabled bool `yaml:"enabled"`

	// WebhookURL is the fallback endpoint; empty disables webhook reports.
	WebhookURL string `yaml:"webhook_url"`

	// SMTP configures the fallback email channel.
	SMTP NotifySMTPSection `yaml:"smtp"`

	// EmailRecipients are the fallback alert mailboxes.
	EmailRecipients []string `yaml:"email_recipients"`

	// Interval is the probe cadence as a duration string.
	Interval string `yaml:"interval"`

	// Threshold is the consecutive-failure count before a report.
	Threshold int `yaml:"threshold"`

	// Timeout bounds one delivery call as a duration string.
	Timeout string `yaml:"timeout"`
}

// AccountingSection configures ADR-0025 resource accounting. Accounting
// is a bounded diagnostic channel: one frame in SampleRate is recorded per
// dimension, reduced locally to a Top-N heavy-hitter view and published
// for cluster aggregation. It must never become a second data sink, so
// every bound here is small and validated.
type AccountingSection struct {
	// Enabled turns the sampler, reporter and aggregator on.
	Enabled bool `yaml:"enabled"`

	// NodeID identifies this node in reports. It defaults to the node
	// identity used by the rest of the platform.
	NodeID string `yaml:"node_id"`

	// SampleRate is the 1/N divisor: one frame in N is recorded.
	SampleRate int `yaml:"sample_rate"`

	// Capacity is the per-dimension heavy-hitter candidate budget.
	Capacity int `yaml:"capacity"`

	// ReportInterval is the accounting window length.
	ReportInterval string `yaml:"report_interval"`

	// TopN is the number of candidates carried per dimension in a report.
	TopN int `yaml:"top_n"`
}

// RulesSection configures rule processing consumers.
type RulesSection struct {
	MaxAckPending  int    `yaml:"max_ack_pending"`
	ReloadInterval string `yaml:"reload_interval"`

	// ScriptTimeout bounds one user-script execution as a duration string.
	ScriptTimeout string `yaml:"script_timeout"`
}

// SeedSection contains fixed identifiers used by the `seed` command so that
// the development environment is deterministic across machines.
type SeedSection struct {
	// TenantID is the UUID of the seeded tenant.
	TenantID string `yaml:"tenant_id"`

	// DeviceID is the UUID of the seeded device.
	DeviceID string `yaml:"device_id"`

	// DeviceToken is the access token credential of the seeded device.
	DeviceToken string `yaml:"device_token"`
}

// Load reads, parses and returns the configuration stored at path. A plain
// file is parsed as-is; a directory is read as a merge of its *.yaml/*.yml
// files in filename order, which lets a deployment layer the non-sensitive
// base (ConfigMap) and a secret overlay (Secret) as two sibling files. It
// is the only supported way to construct a Config.
func Load(path string) (*Config, error) {
	data, err := readConfigLayer(path)
	if err != nil {
		return nil, err
	}

	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if err := c.NATS.TLS.Validate("nats.tls"); err != nil {
		return nil, err
	}
	if err := c.Registry.TLS.Validate("registry.tls"); err != nil {
		return nil, err
	}
	if err := c.validateDeadman(); err != nil {
		return nil, err
	}
	if err := c.validateAccounting(); err != nil {
		return nil, err
	}
	if err := c.validateCredentialCache(); err != nil {
		return nil, err
	}
	if err := c.validateHorizon(); err != nil {
		return nil, err
	}
	if err := c.validateCluster(); err != nil {
		return nil, err
	}
	if err := c.validateSSO(); err != nil {
		return nil, err
	}
	return &c, nil
}

// readConfigLayer loads one YAML file, or merges every *.yaml/*.yml file
// in a directory in filename order. The operator supplies the path via
// the CLI, so gosec file-inclusion warnings do not apply here or below.
//
//nolint:gosec // operator-selected configuration path
func readConfigLayer(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return os.ReadFile(path)
	}
	entries, err := os.ReadDir(path) // already sorted by filename
	if err != nil {
		return nil, err
	}
	merged := map[string]any{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		data, err := os.ReadFile(path + string(os.PathSeparator) + name)
		if err != nil {
			return nil, err
		}
		var layer map[string]any
		if err := yaml.Unmarshal(data, &layer); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		merged = mergeConfigMaps(merged, layer)
	}
	return yaml.Marshal(merged)
}

// mergeConfigMaps deep-merges src into dst: nested maps recurse, scalars
// and sequences in src replace the value in dst. Later config layers win,
// so a Secret overlay only needs the secret keys (e.g.
// postgres.dsn) — the rest of the section stays from the ConfigMap base.
func mergeConfigMaps(dst, src map[string]any) map[string]any {
	for k, v := range src {
		if srcMap, ok := v.(map[string]any); ok {
			if dstMap, ok := dst[k].(map[string]any); ok {
				dst[k] = mergeConfigMaps(dstMap, srcMap)
				continue
			}
		}
		dst[k] = v
	}
	return dst
}

// validateCluster applies defaults and checks the HA-related settings.
func (c *Config) validateCluster() error {
	if c.NATS.JetStreamReplicas == 0 {
		c.NATS.JetStreamReplicas = 1
	}
	switch c.NATS.JetStreamReplicas {
	case 1, 3, 5:
	default:
		return fmt.Errorf("nats.jetstream_replicas must be 1, 3 or 5, got %d",
			c.NATS.JetStreamReplicas)
	}
	switch c.OIDC.StateStore {
	case "", "memory":
		c.OIDC.StateStore = "memory"
	case "nats":
	default:
		return fmt.Errorf("oidc.state_store must be memory or nats, got %q", c.OIDC.StateStore)
	}
	if c.Cluster.Enabled {
		if c.NATS.JetStreamReplicas < 3 {
			return fmt.Errorf("cluster.enabled requires nats.jetstream_replicas >= 3")
		}
		if c.OIDC.StateStore == "memory" {
			c.OIDC.StateStore = "nats"
		}
	}
	if c.Cluster.LeaseTTL == "" {
		c.Cluster.LeaseTTL = "15s"
	}
	if _, err := time.ParseDuration(c.Cluster.LeaseTTL); err != nil {
		return fmt.Errorf("cluster.lease_ttl: %w", err)
	}
	return nil
}

// validateSSO applies defaults and checks the NS ticket mode settings.
func (c *Config) validateSSO() error {
	if !c.SSO.Enabled {
		return nil
	}
	if c.OIDC.Enabled {
		return fmt.Errorf("sso.enabled and oidc.enabled are mutually exclusive")
	}
	if c.SSO.NSBaseURL == "" {
		return fmt.Errorf("sso.ns_base_url is required when sso.enabled=true")
	}
	if _, err := url.Parse(c.SSO.NSBaseURL); err != nil {
		return fmt.Errorf("sso.ns_base_url is not a valid URL: %w", err)
	}
	if c.SSO.ClientID == "" {
		return fmt.Errorf("sso.client_id is required when sso.enabled=true")
	}
	if c.SSO.SSOSecret == "" {
		return fmt.Errorf("sso.sso_secret is required when sso.enabled=true")
	}
	if c.SSO.CallbackPath == "" {
		c.SSO.CallbackPath = "/auth/ns-callback"
	}
	if !strings.HasPrefix(c.SSO.CallbackPath, "/") {
		return fmt.Errorf("sso.callback_path must start with /")
	}
	if c.SSO.TenantID == "" {
		c.SSO.TenantID = "00000000-0000-0000-0000-000000000001"
	}
	if c.SSO.DefaultRole == "" {
		c.SSO.DefaultRole = "READ_ONLY"
	}
	if !validSSOAssignableRole(c.SSO.DefaultRole) {
		return fmt.Errorf("sso.default_role %q is not an assignable role", c.SSO.DefaultRole)
	}
	if c.SSO.GroupRole == nil {
		// Conventional /nereidlink/{tenant}/{role} segments (NS default).
		c.SSO.GroupRole = map[string]string{
			"admins":    "TENANT_ADMIN",
			"engineers": "ENGINEER",
			"read_only": "READ_ONLY",
		}
	}
	for key, role := range c.SSO.GroupRole {
		if key == "" {
			return fmt.Errorf("sso.group_role keys must be non-empty role segments")
		}
		if !validSSOAssignableRole(role) {
			return fmt.Errorf("sso.group_role[%q] = %q is not an assignable role", key, role)
		}
	}
	if c.SSO.ReconcileMemberships == nil {
		on := true
		c.SSO.ReconcileMemberships = &on
	}
	if c.SSO.FrontendBaseURL != "" {
		if _, err := url.Parse(c.SSO.FrontendBaseURL); err != nil {
			return fmt.Errorf("sso.frontend_base_url is not a valid URL: %w", err)
		}
	}
	return nil
}

// validSSOAssignableRole is the set of human roles a ticket may grant;
// PLATFORM_ADMIN and SERVICE_ACCOUNT can never be assigned from a ticket.
func validSSOAssignableRole(role string) bool {
	switch role {
	case "TENANT_ADMIN", "ENGINEER", "CUSTOMER_USER", "READ_ONLY":
		return true
	default:
		return false
	}
}

// validateCredentialCache verifies the optional credential cache section.
// A disabled cache is a complete no-op: all other fields are ignored so an
// all-in-one deployment can never fail because of unused cache settings.
func (c *Config) validateCredentialCache() error {
	cc := c.CredentialCache
	if !cc.Enabled {
		return nil
	}
	if cc.PositiveTTL != "" {
		d, err := time.ParseDuration(cc.PositiveTTL)
		if err != nil {
			return fmt.Errorf("credential_cache: positive_ttl is invalid: %w", err)
		}
		if d <= 0 {
			return fmt.Errorf("credential_cache: positive_ttl must be positive")
		}
	}
	if cc.NegativeTTL != "" {
		d, err := time.ParseDuration(cc.NegativeTTL)
		if err != nil {
			return fmt.Errorf("credential_cache: negative_ttl is invalid: %w", err)
		}
		if d <= 0 {
			return fmt.Errorf("credential_cache: negative_ttl must be positive")
		}
	}
	if cc.MaxCost < 0 {
		return fmt.Errorf("credential_cache: max_cost must not be negative")
	}
	if cc.NumCounters < 0 {
		return fmt.Errorf("credential_cache: num_counters must not be negative")
	}
	return nil
}

// validateAccounting keeps resource accounting bounded. The whole point of
// the 1/N sampler is a fixed memory and publish budget, so an
// unreasonable configuration must fail at load rather than quietly turn
// the accounting path into a second data sink.
func (c *Config) validateAccounting() error {
	a := c.Accounting
	if !a.Enabled {
		return nil
	}
	if a.SampleRate < 1 {
		return fmt.Errorf("accounting: sample_rate must be at least 1")
	}
	// Capacity is per dimension and there are six dimensions, so this cap
	// bounds node memory to a few hundred kilobytes at most.
	if a.Capacity < 1 || a.Capacity > 4096 {
		return fmt.Errorf("accounting: capacity must be between 1 and 4096")
	}
	if a.TopN < 1 || a.TopN > a.Capacity {
		return fmt.Errorf("accounting: top_n must be between 1 and capacity")
	}
	return nil
}

// validateDeadman enforces the independent-fallback invariants: an enabled
// channel needs an endpoint, and its webhook must not be the same one the
// tenant notifier uses.
func (c *Config) validateDeadman() error {
	d := c.Deadman
	if !d.Enabled {
		return nil
	}
	if d.WebhookURL == "" && (!d.SMTP.Enabled || len(d.EmailRecipients) == 0) {
		return fmt.Errorf("deadman: enabled requires a webhook_url or SMTP recipient")
	}
	if d.WebhookURL != "" && d.WebhookURL == c.Notify.WebhookURL {
		return fmt.Errorf("deadman: webhook_url must differ from notify.webhook_url")
	}
	return nil
}

// Validate checks a link TLS section for internal consistency: TLS must be
// enabled to use certificates, a client certificate and key must be paired,
// and peer verification may not be disabled once TLS is on.
func (s LinkTLSSection) Validate(prefix string) error {
	if !s.Enabled {
		if s.CAFile != "" || s.CertFile != "" || s.KeyFile != "" || s.InsecureSkipVerify {
			return fmt.Errorf("%s: tls settings require enabled: true", prefix)
		}
		return nil
	}
	if (s.CertFile == "") != (s.KeyFile == "") {
		return fmt.Errorf("%s: cert_file and key_file must be set together", prefix)
	}
	if s.InsecureSkipVerify && s.CAFile != "" {
		return fmt.Errorf("%s: insecure_skip_verify cannot be combined with ca_file", prefix)
	}
	return nil
}

// TLSConfig builds a hardened TLS configuration for the internal link:
// TLS 1.3 only, per ADR-0003's internal-link profile. It returns nil when
// TLS is disabled so callers can leave the transport plaintext.
func (s LinkTLSSection) TLSConfig() (*tls.Config, error) {
	if !s.Enabled {
		return nil, nil
	}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		// InsecureSkipVerify is an explicit, validated opt-in for local
		// testing only; Load rejects it together with a CA and it defaults
		// to false in every production profile.
		InsecureSkipVerify: s.InsecureSkipVerify, //nolint:gosec // opt-in local testing only
	}
	if s.CAFile != "" {
		pem, err := os.ReadFile(s.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read tls ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("tls ca %s contains no certificates", s.CAFile)
		}
		cfg.RootCAs = pool
	}
	if s.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(s.CertFile, s.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load tls client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

// ServerTLSConfig builds the listener side of the internal link. It
// requires a server certificate and, when CAFile is set, verifies and
// requires client certificates (mutual TLS). It returns nil when TLS is
// disabled.
func (s LinkTLSSection) ServerTLSConfig() (*tls.Config, error) {
	if !s.Enabled {
		return nil, nil
	}
	if s.CertFile == "" || s.KeyFile == "" {
		return nil, fmt.Errorf("server tls requires cert_file and key_file")
	}
	cert, err := tls.LoadX509KeyPair(s.CertFile, s.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load tls server certificate: %w", err)
	}
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
	}
	if s.CAFile != "" {
		pem, err := os.ReadFile(s.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read tls client ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("tls client ca %s contains no certificates", s.CAFile)
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}
