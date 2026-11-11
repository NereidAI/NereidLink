// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// validator.go compiles and applies JSON Schemas. Compiled schemas are cached
// in-process keyed by a hash of the document, so validating a hot connector
// type does not recompile on every desired write. A fresh schema registered
// from an edge hello has new content and therefore a new cache key.

package connectorcatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// compiler builds draft 2020-12 schemas. A fresh compiler is used per
// document because the library caches resources on it and we do not resolve
// external $refs (edge schemas must be self-contained inline documents).
func newCompiler() *jsonschema.Compiler {
	return jsonschema.NewCompiler()
}

// compile parses raw as a JSON Schema draft 2020-12 document. External $refs
// are intentionally unsupported: the schema travels inline in hello and must
// be self-contained, which keeps validation hermetic and network-free.
func compile(raw json.RawMessage) (*jsonschema.Schema, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: empty schema", ErrInvalidSchema)
	}
	// Reject non-object / malformed JSON before handing it to the compiler.
	if err := json.Unmarshal(raw, new(any)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSchema, err)
	}
	compiler := newCompiler()
	url := "inline://connector-config.json"
	if err := compiler.AddResource(url, bytes.NewReader(raw)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSchema, err)
	}
	sch, err := compiler.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSchema, err)
	}
	return sch, nil
}

// cache memoizes compiled schemas by content hash. It is safe for concurrent
// hello registrations and desired writes. The cache is small and bounded by
// the number of distinct registered (type,version) documents; re-registration
// of an identical document reuses the entry.
type cache struct {
	mu     sync.RWMutex
	byHash map[string]*jsonschema.Schema
}

func newCache() *cache {
	return &cache{byHash: map[string]*jsonschema.Schema{}}
}

// key returns a stable content key for a schema document.
func schemaHash(raw json.RawMessage) string {
	sum := sha256.Sum256(bytes.TrimSpace(raw))
	return hex.EncodeToString(sum[:])
}

// compiled returns the compiled schema for raw, compiling and caching it on
// first use.
func (c *cache) compiled(raw json.RawMessage) (*jsonschema.Schema, error) {
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
	// Another goroutine may have compiled the same document meanwhile; keep
	// whichever is cached, they are equivalent.
	if existing, ok := c.byHash[key]; ok {
		c.mu.Unlock()
		return existing, nil
	}
	c.byHash[key] = compiled
	c.mu.Unlock()
	return compiled, nil
}

// validateAgainst compiles schemaRaw (using the cache) and validates configRaw
// against it. An empty config document is marshalled to {} so object-typed
// schemas receive a JSON value rather than EOF.
func (s *Service) validateAgainst(schemaRaw, configRaw json.RawMessage) error {
	sch, err := s.cache.compiled(schemaRaw)
	if err != nil {
		return err
	}
	var doc any
	raw := configRaw
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%w: config is not valid JSON: %v", ErrInvalidSchema, err)
	}
	if err := sch.Validate(doc); err != nil {
		return fmt.Errorf("%w: %v", ErrConfigViolation, err)
	}
	return nil
}

// relaxForPatch turns a full-config schema into a patch schema: an instance
// override is deep-merged over the template, so it is a partial document at
// every nesting level. We recursively drop presence constraints ("required",
// "dependentRequired", "minProperties") at the root and in every subschema
// (properties, $defs, items, allOf/anyOf/oneOf, ...) while keeping type and
// shape constraints (types, enums, ranges, additionalProperties). Recursing
// into $defs means $ref targets are relaxed too. The result is a standalone
// document compiled and cached like any other schema.
func relaxForPatch(raw json.RawMessage) (json.RawMessage, error) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSchema, err)
	}
	relaxNode(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSchema, err)
	}
	return out, nil
}

// presenceKeywords force whole-document completeness and therefore do not
// apply to a partial override document.
var presenceKeywords = map[string]bool{
	"required":          true,
	"dependentRequired": true,
	"minProperties":     true,
}

// relaxNode walks an unmarshalled schema tree deleting presence keywords.
func relaxNode(node any) {
	obj, ok := node.(map[string]any)
	if !ok {
		// Arrays of subschemas (allOf/anyOf/oneOf/prefixItems): relax each.
		if arr, ok := node.([]any); ok {
			for _, item := range arr {
				relaxNode(item)
			}
		}
		return
	}
	for kw := range presenceKeywords {
		delete(obj, kw)
	}
	for key, val := range obj {
		switch key {
		case "properties", "patternProperties", "$defs", "defs":
			if m, ok := val.(map[string]any); ok {
				for _, sub := range m {
					relaxNode(sub)
				}
			}
		case "items", "contains", "propertyNames", "not", "if", "then", "else",
			"additionalProperties", "unevaluatedProperties":
			// additionalProperties may be a boolean (ignore) or a subschema.
			relaxNode(val)
		case "allOf", "anyOf", "oneOf", "prefixItems":
			relaxNode(val)
		}
	}
}
