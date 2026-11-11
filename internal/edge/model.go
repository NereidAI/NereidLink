// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package edge implements the transport-independent part of the
// NereidEdge hello negotiation (ADR-0026, protocol v1): the
// UNIDENTIFIED -> NEGOTIATED handshake, protocol-version intersection
// and required/optional feature gating.
//
// The package deliberately knows nothing about MQTT, NATS or HTTP. An
// ingress decodes a hello frame and calls Negotiator.Negotiate, which
// returns a wire-ready Ack and, on success, records an in-memory
// session. Keeping the negotiation here lets the MQTT broker hook and a
// future NATS-leafnode or REST bootstrap give the exact same answer.
package edge

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Hello statuses returned in Ack.Status. They mirror the v1 protocol
// table: ok / auth_failed / incompatible_version /
// incompatible_features / quota_exceeded.
const (
	StatusOK                   = "ok"
	StatusAuthFailed           = "auth_failed"
	StatusIncompatibleVersion  = "incompatible_version"
	StatusIncompatibleFeatures = "incompatible_features"
	StatusQuotaExceeded        = "quota_exceeded"
)

// ErrInvalidHello means the hello frame is structurally unusable, for
// example a missing edge_id or an unparseable version string.
var ErrInvalidHello = errors.New("invalid edge hello")

// ProtoRange declares an inclusive major.minor range.
type ProtoRange struct {
	// Min is the lowest supported protocol version.
	Min string `json:"min"`

	// Max is the highest supported protocol version.
	Max string `json:"max"`
}

// FeatureSet splits the hello feature advertisement into required and
// optional names.
type FeatureSet struct {
	// Required features must all be supported or negotiation fails.
	Required []string `json:"required"`

	// Optional features are downgraded away when the server lacks them.
	Optional []string `json:"optional"`
}

// SecretTypeAdvert is one edge secret type descriptor an edge advertises
// (ADR-0034): the schema of the credential document the platform will
// deliver to this gateway. The schema is opaque to the edge package; NL
// compiles and stores it in the secret catalog.
type SecretTypeAdvert struct {
	// Type is the credential type key, e.g. "onvif-basic".
	Type string `json:"type"`

	// TypeVersion is the descriptor version; empty means unversioned.
	TypeVersion string `json:"type_version"`

	// FieldsSchema is the self-contained draft 2020-12 credential schema.
	FieldsSchema json.RawMessage `json:"fields_schema,omitempty"`

	// DisplayName optionally carries bilingual display names.
	DisplayName map[string]string `json:"display_name,omitempty"`
}

// SecretTypeResult reports the per-entry outcome of registering edge secret
// type descriptors from a hello. Registration never fails the session.
type SecretTypeResult struct {
	// Type and TypeVersion echo the advertisement.
	Type        string `json:"type"`
	TypeVersion string `json:"type_version"`

	// Accepted reports whether the schema compiled and was stored.
	Accepted bool `json:"accepted"`

	// Reason explains a rejected entry (compile error).
	Reason string `json:"reason,omitempty"`
}

// OperationAdvert is one gateway-level operation descriptor an edge
// advertises (ADR-0035). All fields are data: the platform has no built-in
// operation names and derives risk only from this descriptor.
type OperationAdvert struct {
	// Operation is the operation key the edge understands.
	Operation string `json:"operation"`

	// TypeVersion is the descriptor version; empty means unversioned.
	TypeVersion string `json:"type_version"`

	// DisplayName carries bilingual display names.
	DisplayName map[string]string `json:"display_name,omitempty"`

	// Description carries bilingual explanatory text.
	Description map[string]string `json:"description,omitempty"`

	// Interaction is request-response or one-way.
	Interaction string `json:"interaction,omitempty"`

	// TimeoutMs bounds the edge-side execution; 0 selects the default.
	TimeoutMs int `json:"timeout_ms,omitempty"`

	// Risk is read, normal or dangerous.
	Risk string `json:"risk,omitempty"`

	// ParamSchema is the self-contained draft 2020-12 parameters schema.
	ParamSchema json.RawMessage `json:"param_schema,omitempty"`

	// ResultSchema is the self-contained result schema.
	ResultSchema json.RawMessage `json:"result_schema,omitempty"`
}

// OperationResult reports the per-entry outcome of registering operation
// descriptors from a hello. Registration never fails the session.
type OperationResult struct {
	// Operation and TypeVersion echo the advertisement.
	Operation   string `json:"operation"`
	TypeVersion string `json:"type_version"`

	// Accepted reports whether the descriptor was stored.
	Accepted bool `json:"accepted"`

	// Reason explains a rejected entry.
	Reason string `json:"reason,omitempty"`
}

// ConnectorAdvert is one connector type an edge supports, with the inline
// JSON Schema its config document must satisfy. The schema is opaque to the
// edge package; NL compiles and stores it in the connector catalog.
type ConnectorAdvert struct {
	// Type is the connector type key, e.g. "modbus-tcp".
	Type string `json:"type"`

	// TypeVersion is the plugin/type version; empty means unversioned.
	TypeVersion string `json:"type_version"`

	// ConfigSchema is the self-contained draft 2020-12 JSON Schema.
	ConfigSchema json.RawMessage `json:"config_schema,omitempty"`

	// Protocol optionally declares the open-vocabulary protocol family this
	// connector belongs to ("modbus"). Empty leaves family checks fail-open.
	Protocol string `json:"protocol,omitempty"`

	// Transports optionally lists concrete transport keys in the family.
	Transports []string `json:"transports,omitempty"`
}

// ConnectorSchemaResult reports the per-entry outcome of registering the
// inline schemas from a hello. Schema registration never fails the session;
// rejected entries are reported here.
type ConnectorSchemaResult struct {
	Type        string `json:"type"`
	TypeVersion string `json:"type_version"`
	Accepted    bool   `json:"accepted"`
	Reason      string `json:"reason,omitempty"`
}

// Hello is the Edge -> NL negotiation frame on v1/gateway/hello.
type Hello struct {
	// V is the hello format version; only 1 is understood today.
	V int `json:"v"`

	// EdgeID identifies the connector instance.
	EdgeID string `json:"edge_id"`

	// AgentVersion is the Edge agent build, independent of proto version.
	AgentVersion string `json:"agent_version"`

	// Proto is the Edge-supported protocol-version range.
	Proto ProtoRange `json:"proto"`

	// Features advertises required and optional capabilities.
	Features FeatureSet `json:"features"`

	// Connectors advertises the connector types this edge supports and, for
	// each, the inline JSON Schema its config must satisfy (ADR-0026). NL
	// compiles and stores these on a successful hello; a malformed schema is
	// reported per entry in Ack.ConnectorSchemas but never fails the session.
	Connectors []ConnectorAdvert `json:"connectors,omitempty"`

	// SecretTypes advertises the edge secret type descriptors this gateway
	// supports (ADR-0034). NL compiles and stores them on a successful
	// hello; malformed entries are reported per entry in Ack.SecretTypes
	// but never fail the session.
	SecretTypes []SecretTypeAdvert `json:"secret_types,omitempty"`

	// Operations advertises the gateway-level operation descriptors this
	// gateway supports (ADR-0035). Malformed entries are reported per
	// entry in Ack.Operations but never fail the session.
	Operations []OperationAdvert `json:"operations,omitempty"`

	// ProfileFormats lists, in preference order, the device-profile document
	// formats this gateway can apply (ADR-0038), e.g. "nereid-neutral/1"
	// and "edgex-device-profile/3". Empty means the neutral format only.
	ProfileFormats []string `json:"profile_formats,omitempty"`

	// Inference optionally advertises edge model inference support
	// (ADR-0039): the implemented model contract version and the backend
	// protocol families this gateway can speak. Meaningful only with the
	// "inference" optional feature. The ack echoes the accepted
	// intersection; nil keeps the field off the wire.
	Inference *InferenceAdvert `json:"inference,omitempty"`

	// Resume carries an optional session-resume request.
	Resume ResumeRequest `json:"resume"`

	// ClientTime is the Edge clock in milliseconds, used for skew warning.
	ClientTime int64 `json:"client_time"`

	// ResponseTopic optionally overrides the topic the ack is sent on.
	// Empty selects the built-in default.
	ResponseTopic string `json:"response_topic"`
}

// ResumeRequest asks the server to resume a prior session.
type ResumeRequest struct {
	// SessionID is the prior session identifier.
	SessionID string `json:"session_id"`

	// LastCursor optionally carries the edge's last-seen per-stream cursors.
	// Downlink desired state is full-state and resent idempotently on every
	// (re-)negotiation, so today NL accepts the field for forward
	// compatibility but does not replay a cursor window.
	LastCursor map[string]int64 `json:"last_cursor,omitempty"`
}

// ServerInfo reports the server protocol range and build version.
type ServerInfo struct {
	// Min is the lowest server-supported protocol version.
	Min string `json:"min"`

	// Max is the highest server-supported protocol version.
	Max string `json:"max"`

	// Version is the server build version.
	Version string `json:"server_version"`
}

// UploadLimits describes blob-relay upload constraints.
type UploadLimits struct {
	// MaxMB caps a single upload size.
	MaxMB int `json:"max_mb"`

	// QPS caps upload requests per second.
	QPS int `json:"qps"`
}

// Limits reports the negotiated session operating limits.
type Limits struct {
	// HeartbeatS is the application heartbeat interval in seconds.
	HeartbeatS int `json:"heartbeat_s"`

	// HelloTimeoutS is the bootstrap timeout in seconds.
	HelloTimeoutS int `json:"hello_timeout_s"`

	// Upload carries blob-relay limits.
	Upload UploadLimits `json:"upload"`
}

// TimeInfo answers the hello with the server clock and measured skew.
type TimeInfo struct {
	// ServerTime is the server clock in milliseconds.
	ServerTime int64 `json:"server_time"`

	// OffsetMS is server_time minus client_time in milliseconds.
	OffsetMS int64 `json:"offset_ms"`

	// ClockWarning flags excessive skew; it never rejects negotiation.
	ClockWarning bool `json:"clock_warning"`
}

// ResumeResult reports whether the resume request was accepted.
type ResumeResult struct {
	// Accepted reports whether the prior session was resumed.
	Accepted bool `json:"accepted"`
}

// Ack is the NL -> Edge negotiation response.
type Ack struct {
	// SessionID is the negotiated session identifier.
	SessionID string `json:"session_id"`

	// GatewayID is the platform device UUID resolved from the credential.
	// The edge uses it to fill LinkEpisodeReport.EdgeID; empty on older
	// server builds.
	GatewayID string `json:"gateway_id,omitempty"`

	// Status is one of the Status* constants.
	Status string `json:"status"`

	// Reason carries a human explanation when Status is not ok.
	Reason string `json:"reason,omitempty"`

	// Selected is the chosen protocol version.
	Selected string `json:"selected,omitempty"`

	// Server reports the server range and build.
	Server ServerInfo `json:"server"`

	// Features is the intersection actually enabled for the session.
	Features []string `json:"features"`

	// Limits reports the session operating limits.
	Limits Limits `json:"limits"`

	// Time reports the server clock and skew.
	Time TimeInfo `json:"time"`

	// Resume reports the resume decision.
	Resume ResumeResult `json:"resume"`

	// ConnectorSchemas reports per-entry registration results for the inline
	// schemas advertised in hello. Empty when none were advertised.
	ConnectorSchemas []ConnectorSchemaResult `json:"connector_schemas,omitempty"`

	// SecretTypes reports per-entry registration results for the edge
	// secret descriptors advertised in hello.
	SecretTypes []SecretTypeResult `json:"secret_types,omitempty"`

	// Operations reports per-entry registration results for advertised
	// operation descriptors.
	Operations []OperationResult `json:"operations,omitempty"`

	// ProfileFormats echoes the profile document formats the platform
	// enabled for this session (ADR-0038), intersected with what it can
	// render. Empty when the gateway did not negotiate the profiles feature.
	ProfileFormats []string `json:"profile_formats,omitempty"`

	// Inference echoes the edge model-inference capability the platform
	// accepted for this session (ADR-0039): the contract version and the
	// backend protocol-family intersection. It is populated only when the
	// inference feature was negotiated, the contract is recognized and at
	// least one backend intersects; nil/absent means the edge must not
	// enable inference (no models segment, no edge-inference specs).
	Inference *InferenceAdvert `json:"inference,omitempty"`
}

// Session is one negotiated edge session held in memory.
type Session struct {
	// ID is the session identifier.
	ID string

	// EdgeID is the connector instance.
	EdgeID string

	// TenantID scopes the session.
	TenantID string

	// Proto is the selected protocol version.
	Proto string

	// Features is the enabled feature intersection.
	Features []string

	// NegotiatedAt is when the session was first established.
	NegotiatedAt time.Time

	// LastSeen is the last time the session was (re-)negotiated; it anchors
	// the resume TTL and is advanced on every accepted resume.
	LastSeen time.Time
}

// Config parameterizes the Negotiator server-side policy.
type Config struct {
	// ServerVersion is reported in every ack.
	ServerVersion string

	// Proto is the server-supported protocol-version range.
	Proto ProtoRange

	// SupportedFeatures is the set the server can provide.
	SupportedFeatures []string

	// SupportedProfileFormats is the ordered set of device-profile document
	// formats the server can render (ADR-0038). It is intersected with the
	// gateway's preference to form Ack.ProfileFormats; empty means only the
	// neutral format is offered.
	SupportedProfileFormats []string

	// SupportedInferenceContract is the edge model-inference contract
	// version the cloud accepts (ADR-0039), e.g. InferenceContractModel1.
	// A gateway advertising any other contract is not enabled.
	SupportedInferenceContract string

	// SupportedInferenceBackends is the set of inference protocol families
	// the cloud accepts (ADR-0039), e.g. kserve/openai. The gateway's
	// advertisement is intersected with it preserving the gateway's order;
	// an empty intersection leaves inference disabled.
	SupportedInferenceBackends []string

	// HelloTimeout bounds the bootstrap window.
	HelloTimeout time.Duration

	// Heartbeat is the application heartbeat interval.
	Heartbeat time.Duration

	// MaxUploadMB and UploadQPS are reported in limits.
	MaxUploadMB int
	UploadQPS   int

	// ClockSkewWarn bounds acceptable clock skew.
	ClockSkewWarn time.Duration

	// SessionTTL bounds how long a negotiated session remains resumable
	// after its transport drops. Sessions are in-process on the node that
	// terminated the connection, so resume only works when the edge
	// reconnects to the same node within this window. Zero selects the
	// default; a negative value disables resume (sessions are never reused).
	SessionTTL time.Duration
}

// DefaultConfig returns the v1 baseline policy.
func DefaultConfig() Config {
	return Config{
		ServerVersion:     "nereidlink-v1",
		Proto:             ProtoRange{Min: "1.0", Max: "2.0"},
		SupportedFeatures: SupportedFeatures(),
		// Edge model inference (ADR-0039): accept the model/1 contract and
		// the vendor-neutral KServe v2 and OpenAI protocol families.
		SupportedInferenceContract: InferenceContractModel1,
		SupportedInferenceBackends: []string{InferenceBackendKServe, InferenceBackendOpenAI},
		HelloTimeout:               10 * time.Second,
		Heartbeat:                  30 * time.Second,
		MaxUploadMB:                256,
		UploadQPS:                  20,
		ClockSkewWarn:              5 * time.Second,
		SessionTTL:                 24 * time.Hour,
	}
}

// SupportedFeatures returns the server feature set for protocol v1.
func SupportedFeatures() []string {
	return []string{
		FeatureTelemetry, FeaturePresence, FeatureOTA,
		FeatureShadow, FeatureBlobRelay, FeatureCommand,
		FeatureSecrets, FeatureProfiles, FeatureInference,
	}
}

// Has reports whether the feature appears in either the required or the
// optional advertisement.
func (f FeatureSet) Has(name string) bool {
	for _, candidate := range f.Required {
		if candidate == name {
			return true
		}
	}
	for _, candidate := range f.Optional {
		if candidate == name {
			return true
		}
	}
	return false
}

// Feature name constants.
const (
	FeatureTelemetry = "telemetry"
	FeaturePresence  = "presence"
	FeatureOTA       = "ota"
	FeatureShadow    = "shadow"
	FeatureBlobRelay = "blob-relay"
	FeatureCommand   = "command"
	FeatureSecrets   = "secrets"
	// FeatureProfiles advertises device-profile downlink (ADR-0038). A
	// gateway that does not negotiate it never receives profile frames, so
	// old firmware is unaffected.
	FeatureProfiles = "profiles"
	// FeatureInference advertises edge model inference (ADR-0039): the
	// gateway reports a local model inventory in the manifest models
	// segment and accepts the generic edge-inference connector type. It is
	// always optional; an un-negotiated gateway takes no part in inference
	// semantics.
	FeatureInference = "inference"
)

// Inference contract and backend identifiers the platform understands
// (ADR-0039). Backends name protocol families, never silicon vendors.
const (
	// InferenceContractModel1 is the only model/inference contract
	// version defined today ("model/1").
	InferenceContractModel1 = "model/1"
	// InferenceBackendKServe is the KServe v2 inference protocol family.
	InferenceBackendKServe = "kserve"
	// InferenceBackendOpenAI is the OpenAI-compatible inference protocol
	// family (chat/embeddings).
	InferenceBackendOpenAI = "openai"
)

// InferenceAdvert is the optional hello/ack "inference" capability block
// (ADR-0039). Backends lists protocol families, not concrete server
// instances, and never carries vendor names.
type InferenceAdvert struct {
	// Contract is the implemented model/inference contract version,
	// e.g. InferenceContractModel1 ("model/1").
	Contract string `json:"contract"`
	// Backends lists, in preference order, the supported inference
	// protocol families, e.g. ["kserve","openai"].
	Backends []string `json:"backends,omitempty"`
}

// semver is a parsed major.minor (patch ignored for selection).
type semver struct {
	major int
	minor int
}

// parseSemver decodes a major.minor string. A malformed value yields an
// error so negotiation can reject it explicitly rather than silently
// treating it as zero.
func parseSemver(raw string) (semver, error) {
	parts := strings.Split(strings.TrimSpace(raw), ".")
	if len(parts) < 2 {
		return semver{}, errors.New("expected major.minor")
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return semver{}, err
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return semver{}, err
	}
	if major < 0 || minor < 0 {
		return semver{}, errors.New("negative version component")
	}
	return semver{major: major, minor: minor}, nil
}

// compare orders two versions by major then minor.
func (s semver) compare(other semver) int {
	switch {
	case s.major != other.major:
		if s.major < other.major {
			return -1
		}
		return 1
	case s.minor != other.minor:
		if s.minor < other.minor {
			return -1
		}
		return 1
	default:
		return 0
	}
}

// String renders the canonical major.minor form.
func (s semver) String() string {
	return strconv.Itoa(s.major) + "." + strconv.Itoa(s.minor)
}

// selectVersion implements the protocol intersection rule: pick the
// highest version inside both ranges. A mismatch at the major boundary
// (no overlapping majors) is incompatible.
func selectVersion(edge, server ProtoRange) (string, bool) {
	eMin, err := parseSemver(edge.Min)
	if err != nil {
		return "", false
	}
	eMax, err := parseSemver(edge.Max)
	if err != nil {
		return "", false
	}
	sMin, err := parseSemver(server.Min)
	if err != nil {
		return "", false
	}
	sMax, err := parseSemver(server.Max)
	if err != nil {
		return "", false
	}
	if eMin.compare(eMax) > 0 || sMin.compare(sMax) > 0 {
		return "", false
	}

	// Lower/upper bounds of the intersection.
	low := eMin
	if sMin.compare(low) > 0 {
		low = sMin
	}
	high := eMax
	if sMax.compare(high) < 0 {
		high = sMax
	}
	if low.compare(high) > 0 {
		return "", false
	}
	return high.String(), true
}

// featureIntersection returns the ordered enabled set: every required
// feature must be supported or the second result names the first missing
// one; optional features are kept only when supported.
func featureIntersection(features FeatureSet, supported []string) ([]string, string) {
	have := make(map[string]struct{}, len(supported))
	for _, name := range supported {
		have[name] = struct{}{}
	}

	enabled := make([]string, 0, len(features.Required)+len(features.Optional))
	seen := map[string]struct{}{}
	add := func(name string) {
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		enabled = append(enabled, name)
	}

	for _, name := range features.Required {
		if _, ok := have[name]; !ok {
			return nil, name
		}
		add(name)
	}
	for _, name := range features.Optional {
		if _, ok := have[name]; ok {
			add(name)
		}
	}
	return enabled, ""
}
