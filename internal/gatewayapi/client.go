// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package gatewayapi

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// wsSendBuffer bounds how many frames may queue for one connection before
// it is judged a slow consumer and dropped.
const wsSendBuffer = 64

// wsWriteTimeout bounds a single WebSocket frame write. A peer whose TCP
// window never drains must not hold the fanout path past this deadline.
const wsWriteTimeout = 10 * time.Second

// wsPingTimeout bounds one application ping round trip.
const wsPingTimeout = 10 * time.Second

// subscriptionKey pairs a resource type with its tenant. Every real-time
// resource is tenant-scoped, and one connection can hold grants for multiple
// tenants (a platform admin), so the tenant must participate in every routing
// decision — fanout must never deliver tenant A's frame to a subscription the
// client opened against tenant B.
type subscriptionKey struct {
	resourceType string
	tenant       string
}

// subscription is one registered client subscription.
type subscription struct {
	resourceType string
	tenant       string
	devices      map[string]struct{}
	wildcard     bool
}

// Client wraps one WebSocket connection, its declared device subscriptions
// and per-connection sequencing.
type Client struct {
	conn *websocket.Conn

	mu sync.Mutex

	// subscriptions maps client-local subscription IDs to their record.
	subscriptions map[string]*subscription

	// devices indexes the flattened "resource+tenant -> device id" set used
	// by the device-scoped routing hot path. Keying by tenant keeps a device
	// id from one tenant from matching a frame of a different tenant even if
	// ids were ever to collide.
	devices map[subscriptionKey]map[string]struct{}

	// wildcardTenants records which (resource, tenant) pairs have a wildcard
	// subscription. The gateway only materializes a wildcard after confirming
	// the caller is unrestricted for that tenant, so this never spans tenants.
	wildcardTenants map[subscriptionKey]struct{}

	// seq is the monotonic frame sequence for this connection. M0 numbers
	// globally per connection; cursor-based JetStream replay is added with
	// the resume protocol.
	seq uint64

	// seqMu guards seq. It is deliberately separate from mu: the routing
	// fanout calls nextSeq under the Hub lock, and nextSeq must never block
	// on a network write, which is why send() itself never takes mu.
	seqMu sync.Mutex

	// sendCh carries already-serialized frames to the single writer
	// goroutine. Routing only enqueues here; no network I/O happens while
	// subscription state locks are held. It is never closed (closing it
	// would race enqueuers); done signals teardown instead.
	sendCh chan []byte

	// done is closed once when the connection tears down, unblocking the
	// writer and every enqueuer without closing sendCh.
	done chan struct{}

	// closeOnce tears the connection down once, from either side.
	closeOnce sync.Once

	// tenant anchors quota checks for connections with a single tenant;
	// empty for multi-tenant principals (platform admins), whose
	// subscriptions are not quota gated.
	tenant string
}

// NewClient constructs an empty client state and starts its single writer
// goroutine. onClose is invoked once when the writer exits (send stall,
// write error or shutdown), so the owning hub can unregister the client.
// pingInterval drives application pings from the writer goroutine, which is
// the only goroutine allowed to write: coder/websocket forbids concurrent
// writes and Ping itself performs a write.
func NewClient(conn *websocket.Conn, onClose func(*Client), pingInterval time.Duration) *Client {
	c := &Client{
		conn:            conn,
		subscriptions:   map[string]*subscription{},
		devices:         map[subscriptionKey]map[string]struct{}{},
		wildcardTenants: map[subscriptionKey]struct{}{},
		sendCh:          make(chan []byte, wsSendBuffer),
		done:            make(chan struct{}),
	}
	go c.writeLoop(onClose, pingInterval)
	return c
}

// shutdown signals teardown; the writer exits and onClose unregisters the
// client. sendCh is intentionally never closed: concurrent enqueuers select
// on done instead, which a close of sendCh cannot express without a
// send-on-closed-channel panic. Safe to call repeatedly.
func (c *Client) shutdown() {
	c.closeOnce.Do(func() { close(c.done) })
}

// writeLoop is the only goroutine that writes to the WebSocket, owning both
// frame writes and application pings (coder/websocket forbids concurrent
// writers and Ping writes a control frame). Serializing writes here rather
// than under a mutex shared with routing guarantees that a stalled peer can
// only block this goroutine: each write has a deadline, after which the
// connection is closed, and a full sendCh is treated as a slow consumer.
func (c *Client) writeLoop(onClose func(*Client), pingInterval time.Duration) {
	defer func() {
		if onClose != nil {
			onClose(c)
		}
		_ = c.conn.Close(websocket.StatusPolicyViolation, "write stalled")
	}()
	// The ticker is built only for a positive interval: time.NewTicker
	// panics on zero, and unit-test clients never ping.
	var pingCh <-chan time.Time
	if pingInterval > 0 {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		pingCh = ticker.C
	}
	for {
		select {
		case <-c.done:
			return
		case payload := <-c.sendCh:
			ctx, cancel := context.WithTimeout(context.Background(), wsWriteTimeout)
			err := c.conn.Write(ctx, websocket.MessageText, payload)
			cancel()
			if err != nil {
				return
			}
		case <-pingCh:
			ctx, cancel := context.WithTimeout(context.Background(), wsPingTimeout)
			err := c.conn.Ping(ctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// subscribe registers or replaces a subscription identified by id for one
// tenant. wildcard means the caller is authorized to receive every device of
// that tenant for the resource; otherwise only the listed (already
// scope-filtered) device ids are forwarded.
func (c *Client) subscribe(id, resourceType, tenant string, devices []string, wildcard bool) {
	key := subscriptionKey{resourceType: resourceType, tenant: tenant}

	c.mu.Lock()
	defer c.mu.Unlock()

	if old := c.subscriptions[id]; old != nil {
		oldKey := subscriptionKey{resourceType: old.resourceType, tenant: old.tenant}
		if set, ok := c.devices[oldKey]; ok {
			for device := range old.devices {
				delete(set, device)
			}
			if len(set) == 0 {
				delete(c.devices, oldKey)
			}
		}
		if old.wildcard {
			delete(c.wildcardTenants, oldKey)
		}
	}

	set := make(map[string]struct{}, len(devices))
	for _, device := range devices {
		set[device] = struct{}{}
	}
	c.subscriptions[id] = &subscription{
		resourceType: resourceType,
		tenant:       tenant,
		devices:      set,
		wildcard:     wildcard,
	}
	if wildcard {
		c.wildcardTenants[key] = struct{}{}
	}
	if len(set) > 0 {
		if c.devices[key] == nil {
			c.devices[key] = map[string]struct{}{}
		}
		for device := range set {
			c.devices[key][device] = struct{}{}
		}
	}
}

// hasSubscription reports whether the client already holds a subscription
// with the given id. Replacing an existing subscription never consumes
// more quota.
func (c *Client) hasSubscription(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.subscriptions[id]
	return ok
}

// subscriptionCount returns the number of the client's active
// subscriptions.
func (c *Client) subscriptionCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.subscriptions)
}

// wants reports whether the client subscribed to a specific device of a given
// resource and tenant. The map lookup is allocation-free and keeps the Core
// NATS fanout path cheap.
func (c *Client) wants(resourceType, tenant, deviceID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.devices[subscriptionKey{resourceType: resourceType, tenant: tenant}][deviceID]
	return ok
}

// wantsTenant reports whether the client holds any subscription (device list
// or wildcard) for a tenant-wide resource that carries no device filter.
func (c *Client) wantsTenant(resourceType, tenant string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := subscriptionKey{resourceType: resourceType, tenant: tenant}
	if _, ok := c.wildcardTenants[key]; ok {
		return true
	}
	return len(c.devices[key]) > 0
}

// wantsWildcard reports whether the client holds a tenant-wide wildcard
// subscription for one device-scoped resource and tenant.
func (c *Client) wantsWildcard(resourceType, tenant string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.wildcardTenants[subscriptionKey{resourceType: resourceType, tenant: tenant}]
	return ok
}

// nextSeq returns the next monotonic frame sequence.
func (c *Client) nextSeq() uint64 {
	c.seqMu.Lock()
	defer c.seqMu.Unlock()
	c.seq++
	return c.seq
}

// enqueue hands one pre-serialized frame to the writer goroutine. It never
// blocks and never performs network I/O: a full queue means a slow or dead
// peer, in which case the frame is dropped and shutdown is triggered, which
// makes the writer close the WebSocket after its current write deadline.
func (c *Client) enqueue(payload []byte) {
	select {
	case <-c.done:
		return
	default:
	}
	select {
	case <-c.done:
		return
	case c.sendCh <- payload:
	default:
		// Slow consumer: do not stall fanout. The writer's exit invokes
		// onClose, which unregisters the client from the hub.
		c.shutdown()
		slog.Warn("ws client send buffer full, dropping connection",
			"tenant", c.tenant)
	}
}

// send serializes one JSON data frame and enqueues it. A failed enqueue is
// ignored: the connection is torn down independently.
func (c *Client) send(frame dataFrame) {
	c.enqueue(mustJSON(frame))
}

// sendError writes a normalized error frame using the global code vocabulary.
func (c *Client) sendError(code, message string) {
	c.send(dataFrame{
		Type: "error",
		Data: map[string]string{"code": code, "message": message},
	})
}
