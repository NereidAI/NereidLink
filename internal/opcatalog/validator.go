// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// validator.go validates operation descriptors and compiles their
// parameter/result JSON Schemas, caching compiled schemas by content hash.

package opcatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// schemaCache memoizes compiled operation schemas.
type schemaCache struct {
	mu     sync.RWMutex
	byHash map[string]*jsonschema.Schema
}

func newSchemaCache() *schemaCache {
	return &schemaCache{byHash: map[string]*jsonschema.Schema{}}
}

// compiled compiles and caches one raw schema.
func (c *schemaCache) compiled(raw json.RawMessage) (*jsonschema.Schema, error) {
	keySum := sha256.Sum256(bytes.TrimSpace(raw))
	key := hex.EncodeToString(keySum[:])
	c.mu.RLock()
	sch, ok := c.byHash[key]
	c.mu.RUnlock()
	if ok {
		return sch, nil
	}

	if err := json.Unmarshal(raw, new(any)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidDescriptor, err)
	}
	compiler := jsonschema.NewCompiler()
	url := "inline://gateway-operation.json"
	if err := compiler.AddResource(url, bytes.NewReader(raw)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidDescriptor, err)
	}
	compiled, err := compiler.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidDescriptor, err)
	}
	c.mu.Lock()
	c.byHash[key] = compiled
	c.mu.Unlock()
	return compiled, nil
}

// validate normalizes and validates one descriptor, applying defaults and
// returning the canonicalized form.
func validate(d Descriptor, cache *schemaCache) (Descriptor, error) {
	if d.Operation == "" {
		return Descriptor{}, fmt.Errorf("%w: operation is required", ErrInvalidDescriptor)
	}
	if d.Interaction == "" {
		d.Interaction = InteractionRequestResponse
	}
	if d.Interaction != InteractionRequestResponse && d.Interaction != InteractionOneWay {
		return Descriptor{}, fmt.Errorf("%w: interaction", ErrInvalidDescriptor)
	}
	if d.Risk == "" {
		d.Risk = RiskNormal
	}
	if d.Risk != RiskRead && d.Risk != RiskNormal && d.Risk != RiskDangerous {
		return Descriptor{}, fmt.Errorf("%w: risk", ErrInvalidDescriptor)
	}
	if d.TimeoutMs == 0 {
		d.TimeoutMs = 15000
	}
	if d.TimeoutMs < 100 || d.TimeoutMs > 60000 {
		return Descriptor{}, fmt.Errorf("%w: timeout_ms must be 100..60000", ErrInvalidDescriptor)
	}
	if len(bytes.TrimSpace(d.ParamSchema)) > 0 && !bytes.Equal(bytes.TrimSpace(d.ParamSchema), []byte("{}")) {
		if _, err := cache.compiled(d.ParamSchema); err != nil {
			return Descriptor{}, err
		}
	} else {
		d.ParamSchema = json.RawMessage("{}")
	}
	if len(bytes.TrimSpace(d.ResultSchema)) > 0 && !bytes.Equal(bytes.TrimSpace(d.ResultSchema), []byte("{}")) {
		if _, err := cache.compiled(d.ResultSchema); err != nil {
			return Descriptor{}, err
		}
	} else {
		d.ResultSchema = json.RawMessage("{}")
	}
	return d, nil
}
