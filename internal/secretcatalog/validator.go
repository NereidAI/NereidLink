// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// validator.go compiles and applies edge secret fields JSON Schemas.
// Compiled schemas are cached in-process keyed by a hash of the document,
// so validating credential writes does not recompile on every write.

package secretcatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// compile parses raw as a self-contained JSON Schema draft 2020-12 document.
// External $refs are unsupported: the schema travels inline in hello and
// must be hermetic, which keeps validation network-free.
func compile(raw json.RawMessage) (*jsonschema.Schema, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: empty schema", ErrInvalidSchema)
	}
	// Reject malformed JSON before handing it to the compiler.
	if err := json.Unmarshal(raw, new(any)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSchema, err)
	}
	c := jsonschema.NewCompiler()
	url := "inline://edge-secret-fields.json"
	if err := c.AddResource(url, bytes.NewReader(raw)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSchema, err)
	}
	sch, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSchema, err)
	}
	return sch, nil
}

// schemaCache memoizes compiled schemas by content hash. It is safe for
// concurrent hello registrations and credential writes.
type schemaCache struct {
	mu     sync.RWMutex
	byHash map[string]*jsonschema.Schema
}

func newSchemaCache() *schemaCache {
	return &schemaCache{byHash: map[string]*jsonschema.Schema{}}
}

func schemaHash(raw json.RawMessage) string {
	sum := sha256.Sum256(bytes.TrimSpace(raw))
	return hex.EncodeToString(sum[:])
}

// compiled returns the compiled schema for raw, compiling and caching it on
// first use.
func (c *schemaCache) compiled(raw json.RawMessage) (*jsonschema.Schema, error) {
	key := schemaHash(raw)
	c.mu.RLock()
	sch, ok := c.byHash[key]
	c.mu.RUnlock()
	if ok {
		return sch, nil
	}
	compiled, err := compile(raw)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	// Another goroutine may have compiled the same document meanwhile;
	// keep whichever is cached, they are equivalent.
	if existing, ok := c.byHash[key]; ok {
		c.mu.Unlock()
		return existing, nil
	}
	c.byHash[key] = compiled
	c.mu.Unlock()
	return compiled, nil
}

// validateAgainst compiles schemaRaw (using the cache) and validates docRaw
// against it. An empty document is normalized to {} first.
func validateAgainst(cache *schemaCache, schemaRaw, docRaw json.RawMessage) error {
	sch, err := cache.compiled(schemaRaw)
	if err != nil {
		return err
	}
	var doc any
	raw := docRaw
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%w: document is not valid JSON: %v", ErrInvalidSchema, err)
	}
	if err := sch.Validate(doc); err != nil {
		return fmt.Errorf("%w: %v", ErrFieldsViolation, err)
	}
	return nil
}
