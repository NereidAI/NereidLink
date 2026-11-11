// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package profile

import (
	"container/list"
	"sync"
	"time"
)

// ttlLRU is a time-bounded least-recently-used cache. It keeps hot profile
// metadata off the database without unbounded memory growth across devices.
type ttlLRU struct {
	mu      sync.Mutex
	ttl     time.Duration
	maxSize int
	items   map[string]*list.Element
	order   *list.List
}

// cacheEntry is one cached value with an absolute expiration time.
type cacheEntry struct {
	key     string
	expires time.Time
	value   any
}

// newTTLLRU constructs a cache.
func newTTLLRU(ttl time.Duration, maxSize int) *ttlLRU {
	return &ttlLRU{
		ttl:     ttl,
		maxSize: maxSize,
		items:   map[string]*list.Element{},
		order:   list.New(),
	}
}

// get returns a fresh value and records recent use.
func (c *ttlLRU) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.items[key]
	if !ok {
		return nil, false
	}
	entry := element.Value.(*cacheEntry)
	if time.Now().After(entry.expires) {
		c.removeElement(element)
		return nil, false
	}
	c.order.MoveToFront(element)
	return entry.value, true
}

// set stores a value and evicts expired or least-recently-used entries.
func (c *ttlLRU) set(key string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if element, ok := c.items[key]; ok {
		element.Value.(*cacheEntry).expires = time.Now().Add(c.ttl)
		element.Value.(*cacheEntry).value = value
		c.order.MoveToFront(element)
		return
	}

	entry := &cacheEntry{
		key:     key,
		expires: time.Now().Add(c.ttl),
		value:   value,
	}
	element := c.order.PushFront(entry)
	c.items[key] = element

	for c.order.Len() > c.maxSize {
		c.removeElement(c.order.Back())
	}
}

// removeElement removes one list element and its map entry.
func (c *ttlLRU) removeElement(element *list.Element) {
	if element == nil {
		return
	}
	entry := element.Value.(*cacheEntry)
	delete(c.items, entry.key)
	c.order.Remove(element)
}
