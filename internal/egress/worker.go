// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// worker.go runs the in-process delivery workers. It periodically reconciles
// the desired set of forwarders against running consumers: start a consumer
// for a newly enabled forwarder, restart it when its configuration changes,
// stop it when disabled, and stop and delete its stream when the forwarder
// row is deleted. Every running consumer shares the same guarantees — a
// bounded delivery pool, a hard per-call deadline, a circuit breaker, capped
// exponential backoff and a move to the DLQ on terminal/exhausted messages.

package egress

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
)

// Default worker tuning.
const (
	// consumerDurable is the shared durable name; replicas load-balance.
	consumerDurable = "egress-worker"
	// defaultWorkers bounds concurrent deliveries per forwarder.
	defaultWorkers = 4
	// defaultDeliveryTimeout bounds one sink call.
	defaultDeliveryTimeout = 10 * time.Second
	// defaultMaxAckPending bounds in-flight messages per consumer.
	defaultMaxAckPending = 32
	// defaultReconcileInterval is the desired-set poll.
	defaultReconcileInterval = 10 * time.Second
	// baseBackoff starts the retry ladder.
	baseBackoff = 2 * time.Second
	// maxBackoff caps the retry delay.
	maxBackoff = 2 * time.Minute
	// ackWait bounds how long JetStream waits before redelivering an
	// unacked message, comfortably above the delivery timeout.
	ackWait = time.Minute
)

// WorkerTuning overrides the worker defaults; zero values use defaults.
type WorkerTuning struct {
	// Workers bounds concurrent deliveries per forwarder.
	Workers int
	// DeliveryTimeout bounds one sink call.
	DeliveryTimeout time.Duration
	// MaxAckPending bounds unacked in-flight messages.
	MaxAckPending int
	// ReconcileInterval bounds the desired-set poll interval.
	ReconcileInterval time.Duration
}

// runner is one running forwarder consumer.
type runner struct {
	f        Forwarder
	sink     Sink
	breaker  *breaker
	sem      chan struct{}
	wg       sync.WaitGroup
	baseCtx  context.Context
	cancel   context.CancelFunc
	stopCons jetstream.ConsumeContext
}

// workerStore is the persistence surface the worker needs; satisfied by
// *Store and narrowed so reconcile/start locking is testable without a
// database.
type workerStore interface {
	ListAll(context.Context) ([]Forwarder, error)
	InsertDLQ(context.Context, *DeadLetter) error
	InsertDelivery(context.Context, *Delivery) error
	PruneDeliveries(ctx context.Context, olderThan time.Time, capPerForwarder int) error
}

// Worker reconciles and runs forwarder consumers.
type Worker struct {
	js       jetstream.JetStream
	store    workerStore
	tune     WorkerTuning
	replicas int
	baseCtx  context.Context

	mu      sync.Mutex
	running map[string]*runner
}

// NewWorker constructs an egress worker. replicas is the JetStream Raft
// factor (1 for the bundled single NATS, 3/5 clustered).
func NewWorker(js jetstream.JetStream, store *Store, tune WorkerTuning, replicas int) *Worker {
	if replicas < 1 {
		replicas = 1
	}
	return &Worker{js: js, store: store, tune: tune, replicas: replicas, running: map[string]*runner{}}
}

// Run blocks reconciling consumers until ctx is canceled. In-flight
// deliveries are allowed to finish before their consumer is stopped.
func (w *Worker) Run(ctx context.Context) error {
	w.baseCtx = ctx
	interval := w.tune.ReconcileInterval
	if interval <= 0 {
		interval = defaultReconcileInterval
	}

	if err := w.reconcile(ctx); err != nil {
		slog.Error("egress initial reconcile failed", "error", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	pruneTicker := time.NewTicker(deliveryPruneInterval)
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.stopAll()
			return nil
		case <-ticker.C:
			if err := w.reconcile(ctx); err != nil {
				slog.Error("egress reconcile failed", "error", err)
			}
		case <-pruneTicker.C:
			// Bound the delivery log by age and per-forwarder count. A
			// prune failure is retried on the next tick and never affects
			// delivery.
			cutoff := time.Now().Add(-deliveryRetention)
			if err := w.store.PruneDeliveries(ctx, cutoff, deliveryCapPerForwarder); err != nil {
				slog.Error("egress delivery prune failed", "error", err)
			}
		}
	}
}

// reconcile aligns running consumers with the stored forwarder set.
func (w *Worker) reconcile(ctx context.Context) error {
	all, err := w.store.ListAll(ctx)
	if err != nil {
		return err
	}
	desired := map[string]Forwarder{}
	for _, f := range all {
		if f.Enabled {
			desired[f.ID] = f
		}
	}

	// Locking rule: w.mu guards only the running map. start performs
	// network setup (stream/consumer creation) and must never run under it,
	// because start re-locks w.mu to publish the runner and Go's Mutex is
	// non-reentrant; calling it here under the lock self-deadlocks the
	// worker forever.
	toStop := []*runner{}
	toStart := []Forwarder{}
	w.mu.Lock()
	for id, r := range w.running {
		f, wanted := desired[id]
		if !wanted {
			toStop = append(toStop, r)
			delete(w.running, id)
			continue
		}
		// Configuration changed: restart to rebuild sink and consumer.
		if !f.UpdatedAt.Equal(r.f.UpdatedAt) {
			toStop = append(toStop, r)
			delete(w.running, id)
		}
	}
	for id, f := range desired {
		if _, exists := w.running[id]; !exists {
			toStart = append(toStart, f)
		}
	}
	w.mu.Unlock()

	// Create new consumers outside the lock. start inserts the finished
	// runner with its own short critical section. Reconcile is driven by a
	// single Run loop, so two reconciles never overlap; a failed start is
	// retried on the following cycle.
	for _, f := range toStart {
		if err := w.start(ctx, f); err != nil {
			slog.Error("start egress consumer failed",
				"forwarder", f.ID, "error", err)
		}
	}

	// Stop removed/changed consumers after new ones are up. A forwarder
	// that disappeared (not merely disabled) also has its bounded stream
	// deleted.
	for _, r := range toStop {
		r.stop()
		if _, present := desired[r.f.ID]; !present {
			if err := DeleteStream(ctx, w.js, r.f.TenantID, r.f.ID); err != nil {
				slog.Error("delete egress stream failed",
					"forwarder", r.f.ID, "error", err)
			}
		}
	}
	return nil
}

// start ensures the stream and begins consuming one forwarder.
func (w *Worker) start(ctx context.Context, f Forwarder) error {
	// Fail closed when no JetStream is wired; calling a nil JetStream
	// would panic inside EnsureStream and wedge reconcile.
	if w.js == nil {
		return errors.New("egress worker: jetstream not configured")
	}
	if err := EnsureStream(ctx, w.js, f, w.replicas); err != nil {
		return err
	}
	sink, err := buildSink(f)
	if err != nil {
		return err
	}

	baseCtx, cancel := context.WithCancel(w.baseCtx)
	r := &runner{
		f:       f,
		sink:    sink,
		breaker: newBreaker(),
		sem:     make(chan struct{}, w.workers()),
		baseCtx: baseCtx,
		cancel:  cancel,
	}

	cons, err := w.js.CreateOrUpdateConsumer(ctx, StreamName(f.TenantID, f.ID), jetstream.ConsumerConfig{
		Durable:       consumerDurable,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       ackWait,
		MaxAckPending: w.maxAckPending(),
	})
	if err != nil {
		cancel()
		return err
	}
	stopCons, err := cons.Consume(func(msg jetstream.Msg) {
		w.onMessage(r, msg)
	})
	if err != nil {
		cancel()
		return err
	}
	r.stopCons = stopCons

	// A shutdown arriving during setup must not leak the runner: stopAll
	// only walks the map, so an unregistered runner would never stop.
	w.mu.Lock()
	if ctx.Err() != nil {
		w.mu.Unlock()
		r.stop()
		return ctx.Err()
	}
	w.running[f.ID] = r
	w.mu.Unlock()
	return nil
}

// onMessage delivers one message and settles it.
func (w *Worker) onMessage(r *runner, msg jetstream.Msg) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("egress delivery panicked",
				"forwarder", r.f.ID, "panic", rec)
		}
	}()

	select {
	case r.sem <- struct{}{}:
	case <-r.baseCtx.Done():
		// Shutting down before delivery; leave the message unacked so it is
		// redelivered to another consumer or after restart.
		return
	}
	r.wg.Add(1)
	defer func() {
		r.wg.Done()
		<-r.sem
	}()

	var env Envelope
	if err := json.Unmarshal(msg.Data(), &env); err != nil {
		// A malformed payload can never succeed; park it rather than loop.
		w.recordDelivery(r, Envelope{}, Outcome{
			Result: ResultTerminal,
			Reason: "decode envelope: " + err.Error(),
		}, 1)
		if w.park(r, Envelope{}, "decode envelope: "+err.Error(), 1) {
			_ = msg.Term()
		} else {
			_ = msg.Nak()
		}
		return
	}

	attempts := uint64(1)
	if md, err := msg.Metadata(); err == nil {
		attempts = md.NumDelivered
	}

	if !r.breaker.allow() {
		// Receiver is failing continuously; delay redelivery by roughly the
		// cooldown instead of issuing another call.
		_ = msg.NakWithDelay(breakerCooldown)
		return
	}

	dctx, cancel := context.WithTimeout(r.baseCtx, w.deliveryTimeout())
	defer cancel()
	out := r.sink.Deliver(dctx, env)
	r.breaker.recordResult(out.Result == ResultDelivered)
	w.recordDelivery(r, env, out, attempts)

	switch out.Result {
	case ResultDelivered:
		_ = msg.Ack()

	case ResultTerminal:
		if w.park(r, env, out.Reason, attempts) {
			_ = msg.Term()
		} else {
			_ = msg.Nak()
		}

	default: // ResultRetryable
		if int(attempts) >= r.f.MaxAttempts {
			if w.park(r, env, "retries exhausted ("+out.Reason+")", attempts) {
				_ = msg.Term()
			} else {
				_ = msg.Nak()
			}
			return
		}
		_ = msg.NakWithDelay(backoffDelay(attempts))
	}
}

// park inserts one dead letter. On an insert failure the message is NOT
// settled so it is redelivered and never silently lost.
func (w *Worker) park(r *runner, env Envelope, reason string, attempts uint64) bool {
	now := time.Now()
	// NumDelivered is bounded by MaxAttempts (max 50) before parking, so
	// the conversion to int cannot overflow.
	attemptCount := int(attempts) //nolint:gosec // G115: attempts is bounded by max_attempts (<=50)
	dlq := &DeadLetter{
		ID:          uuid.NewString(),
		TenantID:    r.f.TenantID,
		ForwarderID: r.f.ID,
		Envelope:    env,
		Error:       reason,
		Attempts:    attemptCount,
		LastTriedAt: &now,
	}
	if err := w.store.InsertDLQ(r.baseCtx, dlq); err != nil {
		slog.Error("egress dlq insert failed",
			"forwarder", r.f.ID, "error", err)
		return false
	}
	return true
}

// recordDelivery best-effort appends one delivery-log row capturing the
// receiver return code. A logging failure is logged but never changes
// settlement, so a log outage cannot stall or lose a delivery.
func (w *Worker) recordDelivery(r *runner, env Envelope, out Outcome, attempts uint64) {
	// NumDelivered is bounded by MaxAttempts (max 50) before parking, so
	// the conversion to int cannot overflow.
	attemptCount := int(attempts) //nolint:gosec // G115: attempts is bounded by max_attempts (<=50)
	d := &Delivery{
		ID:          uuid.NewString(),
		TenantID:    r.f.TenantID,
		ForwarderID: r.f.ID,
		EnvelopeID:  env.ID,
		DeviceID:    env.DeviceID,
		Status:      out.Result.String(),
		Code:        out.Code,
		Reason:      out.Reason,
		Attempts:    attemptCount,
		OccurredAt:  time.Now(),
	}
	if err := w.store.InsertDelivery(r.baseCtx, d); err != nil {
		slog.Error("egress delivery log insert failed",
			"forwarder", r.f.ID, "error", err)
	}
}

// stop ends new deliveries and waits for in-flight work to finish.
func (r *runner) stop() {
	if r.stopCons != nil {
		r.stopCons.Stop()
	}
	r.wg.Wait()
	r.cancel()
	// Release any persistent broker/client resources. The HTTP sink holds
	// none, but stateful sinks do; close them so a rebuild or removal does
	// not leak a connection and its goroutines.
	if closer, ok := r.sink.(sinkCloser); ok {
		_ = closer.Close()
	}
}

// sinkCloser is the optional resource-release interface implemented by
// stateful sinks.
type sinkCloser interface {
	Close() error
}

// stopAll stops every running consumer (shutdown).
func (w *Worker) stopAll() {
	w.mu.Lock()
	runners := make([]*runner, 0, len(w.running))
	for _, r := range w.running {
		runners = append(runners, r)
	}
	w.running = map[string]*runner{}
	w.mu.Unlock()
	for _, r := range runners {
		r.stop()
	}
}

// backoffDelay returns a capped exponential delay with jitter.
func backoffDelay(attempt uint64) time.Duration {
	d := baseBackoff << (attempt - 1)
	if d <= 0 || d > maxBackoff {
		d = maxBackoff
	}
	// Random factor in 0.8..1.2 to spread retried calls; non-crypto
	// randomness is sufficient for retry jitter.
	factor := 80 + rand.Int63n(41) //nolint:gosec // G404: jitter does not need cryptographic randomness
	return time.Duration(int64(d) * factor / 100)
}

// workers returns the bounded concurrency for this worker.
func (w *Worker) workers() int {
	if w.tune.Workers > 0 {
		return w.tune.Workers
	}
	return defaultWorkers
}

// maxAckPending returns the in-flight cap.
func (w *Worker) maxAckPending() int {
	if w.tune.MaxAckPending > 0 {
		return w.tune.MaxAckPending
	}
	return defaultMaxAckPending
}

// deliveryTimeout returns the per-call deadline.
func (w *Worker) deliveryTimeout() time.Duration {
	if w.tune.DeliveryTimeout > 0 {
		return w.tune.DeliveryTimeout
	}
	return defaultDeliveryTimeout
}
