// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// processor.go consumes partitioned telemetry JetStream streams, resolves
// the chain group bound to each device (device override, profile binding,
// tenant default) and executes its active version, acknowledging handled
// messages.

package rule

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/internal/streams"
)

// FrameDecoder decompresses a bus message produced by buscodec.Codec.
type FrameDecoder interface {
	// Decode returns the original envelope bytes.
	Decode(raw []byte) ([]byte, error)
}

// Processor runs one consumer per telemetry stream shard.
type Processor struct {
	js         jetstream.JetStream
	chainStore *ChainStore
	deps       Deps
	debug      *DebugStore

	// decoder transparently decompresses bus frames (ADR-0003); nil is
	// passthrough for compression off. Without it compressed envelopes
	// fail unmarshal and are acked-and-dropped, so chains never fire.
	decoder FrameDecoder

	maxAckPending  int
	reloadInterval time.Duration

	mu     sync.Mutex
	chains map[string]*cachedChain
}

// cachedChain is one compiled active chain per device.
type cachedChain struct {
	groupID string
	chainID string
	graph   *Graph
	checked time.Time
}

// ProcessorTuning configures consumer behavior.
type ProcessorTuning struct {
	// MaxAckPending bounds unacknowledged messages per shard consumer.
	MaxAckPending int

	// ReloadInterval bounds active-chain identity checks per tenant.
	ReloadInterval time.Duration
}

// SetFrameDecoder wires transparent bus decompression. It must be given
// the same codec instance as the ingress SetEncoder when bus compression
// is enabled.
func (p *Processor) SetFrameDecoder(decoder FrameDecoder) {
	p.decoder = decoder
}

// NewProcessor constructs the rule processor.
func NewProcessor(js jetstream.JetStream, chainStore *ChainStore, deps Deps, debug *DebugStore, tuning ProcessorTuning) *Processor {
	return &Processor{
		js:             js,
		chainStore:     chainStore,
		deps:           deps,
		debug:          debug,
		maxAckPending:  tuning.MaxAckPending,
		reloadInterval: tuning.ReloadInterval,
		chains:         map[string]*cachedChain{},
	}
}

// Run starts one ordered consumer per shard and blocks until ctx canceled.
func (p *Processor) Run(ctx context.Context, shards int) error {
	consumeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for shard := 0; shard < shards; shard++ {
		//nolint:gosec // shard is bounded by the fixed StreamShards count
		if err := p.startConsumer(consumeCtx, shard); err != nil {
			return err
		}
	}

	<-ctx.Done()
	return nil
}

// startConsumer creates the durable ordered consumer for one shard stream.
func (p *Processor) startConsumer(ctx context.Context, shard int) error {
	//nolint:gosec // shard is bounded by the fixed StreamShards count
	stream := streams.TelemetryStreamName(uint32(shard))
	consumer, err := p.js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Name:          fmt.Sprintf("RP_S%d", shard),
		Durable:       fmt.Sprintf("RP_S%d", shard),
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		MaxAckPending: p.maxAckPending,
	})
	if err != nil {
		return fmt.Errorf("create rule consumer for %s: %w", stream, err)
	}

	handler := func(msg jetstream.Msg) {
		// A consumer goroutine panic kills the whole process (and, with
		// durable consumers, crash-loops every replica that takes the
		// shard). Never let a single poison frame do that: log it and
		// nak with a delay so the message is retried/inspectable instead
		// of being silently terminated.
		defer func() {
			if r := recover(); r != nil {
				slog.Error("rule processor handler panic",
					"shard", shard, "error", r,
					"stack", string(debug.Stack()))
				_ = msg.NakWithDelay(10 * time.Second)
			}
		}()
		p.handle(ctx, msg)
	}
	if _, err := consumer.Consume(handler); err != nil {
		return fmt.Errorf("consume %s: %w", stream, err)
	}
	return nil
}

// handle executes one telemetry message through the tenant active chain.
// decodeBusFrame applies the optional buscodec decompression. A nil
// decoder is passthrough, so the processor keeps working with
// compression disabled.
func (p *Processor) decodeBusFrame(raw []byte) ([]byte, error) {
	if p.decoder == nil {
		return raw, nil
	}
	return p.decoder.Decode(raw)
}

func (p *Processor) handle(ctx context.Context, msg jetstream.Msg) {
	raw := msg.Data()
	// The bus may carry compressed envelopes (ADR-0003); the persistence
	// consumer decodes with the shared buscodec and the rule path must too.
	decoded, err := p.decodeBusFrame(raw)
	if err != nil {
		slog.Warn("rule processor bad buscodec frame", "error", err)
		_ = msg.Ack()
		return
	}
	var env v1.Envelope
	if err := protoUnmarshal(decoded, &env); err != nil {
		slog.Warn("rule processor bad envelope", "error", err)
		_ = msg.Ack()
		return
	}

	var point v1.TelemetryPoint
	if err := protoUnmarshal(env.Payload, &point); err != nil {
		_ = msg.Ack()
		return
	}

	graph, err := p.graphFor(ctx, env.TenantId, env.DeviceId)
	if err != nil {
		if err == ErrNoActiveChain {
			_ = msg.Ack()
			return
		}
		slog.Error("rule chain resolve failed", "tenant", env.TenantId,
			"device", env.DeviceId, "error", err)
		_ = msg.NakWithDelay(2 * time.Second)
		return
	}

	data := map[string]any{
		"measurement_key": point.MeasurementKey,
		"v_double":        point.VDouble,
		"raw_value":       point.RawDouble,
	}
	if point.VString != "" {
		data["v_string"] = point.VString
		data[point.MeasurementKey] = point.VString
	} else {
		data[point.MeasurementKey] = point.VDouble
	}
	in := Msg{
		Type:     "telemetry",
		TenantID: env.TenantId,
		DeviceID: env.DeviceId,
		Ts:       env.EventTime,
		TraceID:  uuid.NewString(),
		Data:     data,
		Metadata: map[string]any{
			"measurement_key":    point.MeasurementKey,
			"quality":            point.Quality,
			"unit":               point.Unit,
			"descriptor_version": point.DescriptorVersion,
		},
	}

	// Defence in depth: graphFor owns the (graph, error) contract, but a
	// nil graph with nil error must never reach Execute and panic.
	if graph == nil {
		_ = msg.Ack()
		return
	}

	trace, runErr := graph.Execute(ctx, in, false, p.debug)
	if runErr != nil {
		slog.Error("rule chain execution failed",
			"tenant", env.TenantId, "trace", in.TraceID, "error", runErr)
	}
	if p.debug != nil {
		p.debug.RecordTrace(trace)
	}
	_ = msg.Ack()
}

// graphFor returns the compiled chain bound to a device, re-resolving the
// binding at most every reload interval.
func (p *Processor) graphFor(ctx context.Context, tenantID, deviceID string) (*Graph, error) {
	p.mu.Lock()
	cached := p.chains[deviceID]
	p.mu.Unlock()

	if cached != nil && time.Since(cached.checked) < p.reloadInterval {
		// A nil graph is the negative cache written for ErrNoActiveChain.
		// Returning (nil, nil) here would make the caller invoke
		// (*Graph).Execute on a nil receiver and panic the process.
		if cached.graph == nil {
			return nil, ErrNoActiveChain
		}
		return cached.graph, nil
	}

	groupID, chainID, dto, err := p.chainStore.ChainForDevice(ctx, tenantID, deviceID)
	if err != nil {
		if err == ErrNoActiveChain {
			p.mu.Lock()
			p.chains[deviceID] = &cachedChain{checked: time.Now()}
			p.mu.Unlock()
		}
		return nil, err
	}

	if cached != nil && cached.chainID == chainID {
		cached.checked = time.Now()
		if cached.graph == nil {
			return nil, ErrNoActiveChain
		}
		return cached.graph, nil
	}

	graph, err := Compile(chainID, dto, p.deps)
	if err != nil {
		return nil, fmt.Errorf("compile active chain: %w", err)
	}

	p.mu.Lock()
	p.chains[deviceID] = &cachedChain{
		groupID: groupID,
		chainID: chainID,
		graph:   graph,
		checked: time.Now(),
	}
	p.mu.Unlock()
	return graph, nil
}

// InvalidateGroup drops the cached chain of every device resolved to the
// given group, used after activation and binding changes.
func (p *Processor) InvalidateGroup(groupID string) {
	p.mu.Lock()
	for deviceID, cached := range p.chains {
		if cached.groupID == groupID {
			delete(p.chains, deviceID)
		}
	}
	p.mu.Unlock()
}

// Invalidate drops the cached chain of a device.
func (p *Processor) Invalidate(deviceID string) {
	p.mu.Lock()
	delete(p.chains, deviceID)
	p.mu.Unlock()
}

// SetNodeDebug toggles capture for one node.
func (p *Processor) SetNodeDebug(nodeID string, enabled bool) {
	p.debug.SetNodeDebug(nodeID, enabled)
}

// NodeCaptures returns recent captures of one node.
func (p *Processor) NodeCaptures(nodeID string) []Capture {
	return p.debug.Captures(nodeID)
}

// SetChainTrace toggles full-chain tracing.
func (p *Processor) SetChainTrace(chainID string, enabled bool) {
	p.debug.SetChainTrace(chainID, enabled)
}

// ActiveChainID returns the active chain identifier of the tenant default
// group.
func (p *Processor) ActiveChainID(ctx context.Context, tenantID string) (string, error) {
	groupID, err := p.chainStore.DefaultGroupID(ctx, tenantID)
	if err != nil {
		return "", err
	}
	chainID, _, err := p.chainStore.ActiveChainForGroup(ctx, groupID)
	return chainID, err
}

// ChainTraces returns recent traces optionally filtered by trace ID.
func (p *Processor) ChainTraces(chainID, traceID string) []Trace {
	return p.debug.Traces(chainID, traceID)
}

// DryRunGraph executes one constructed message through a caller-supplied
// graph with side effects suppressed. It lets the visual editor test unsaved
// changes without requiring those changes to be activated first.
func (p *Processor) DryRunGraph(ctx context.Context, dto GraphDTO, data, metadata map[string]any) (Trace, error) {
	graph, err := Compile(uuid.NewString(), dto, p.deps)
	if err != nil {
		return Trace{}, err
	}

	if metadata == nil {
		metadata = map[string]any{}
	}
	msg := Msg{
		Type:     "telemetry",
		TenantID: "",
		Ts:       time.Now().UnixMilli(),
		TraceID:  uuid.NewString(),
		Data:     data,
		Metadata: metadata,
	}
	return graph.Execute(ctx, msg, true, p.debug)
}

// DryRun executes one constructed message through the active chain of the
// tenant default group with side effects suppressed and returns the trace.
func (p *Processor) DryRun(ctx context.Context, tenantID string, data, metadata map[string]any) (Trace, error) {
	groupID, err := p.chainStore.DefaultGroupID(ctx, tenantID)
	if err != nil {
		if err == ErrNoActiveChain {
			return Trace{}, nil
		}
		return Trace{}, err
	}
	chainID, dto, err := p.chainStore.ActiveChainForGroup(ctx, groupID)
	if err != nil {
		if err == ErrNoActiveChain {
			return Trace{}, nil
		}
		return Trace{}, err
	}
	graph, err := Compile(chainID, dto, p.deps)
	if err != nil {
		return Trace{}, err
	}

	if metadata == nil {
		metadata = map[string]any{}
	}
	msg := Msg{
		Type:     "telemetry",
		TenantID: tenantID,
		Ts:       time.Now().UnixMilli(),
		TraceID:  uuid.NewString(),
		Data:     data,
		Metadata: metadata,
	}
	return graph.Execute(ctx, msg, true, p.debug)
}
