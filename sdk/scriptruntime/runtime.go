// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package scriptruntime implements the unified ScriptRuntime contract from
// ADR-0015. The MVP runs JavaScript in-process through goja (ES2015 subset);
// the same interface is later backed by an external script-runner role for
// cluster isolation, without changing the rule engine.
package scriptruntime

import (
	"errors"
	"fmt"
	"time"

	"github.com/dop251/goja"
)

// ErrScriptFailed wraps a user-script exception so nodes can distinguish a
// bad script from platform failures.
var ErrScriptFailed = errors.New("script execution failed")

// Runtime evaluates user JavaScript. Each evaluation is isolated in its own
// goja VM, which is safe for concurrent use and prevents user state from
// leaking between messages.
type Runtime struct {
	// executionTimeout bounds a single evaluation so a looping script
	// cannot stall a processing partition.
	executionTimeout time.Duration
}

// New constructs an in-process script runtime.
func New(executionTimeout time.Duration) *Runtime {
	return &Runtime{executionTimeout: executionTimeout}
}

// EvalBoolean runs source with (msg, metadata) in scope and requires a
// boolean result. It is used by filter and switch conditions.
func (r *Runtime) EvalBoolean(source string, msg, metadata map[string]any) (bool, error) {
	value, err := r.eval(source, msg, metadata)
	if err != nil {
		return false, err
	}
	result, ok := value.Export().(bool)
	if !ok {
		return false, fmt.Errorf("%w: result is not boolean: %s", ErrScriptFailed, value)
	}
	return result, nil
}

// EvalValue runs source with a message object and returns the exported
// result. It is used by measurement descriptor expressions that may return
// a number or a string.
func (r *Runtime) EvalValue(source string, msg map[string]any) (any, error) {
	value, err := r.eval(source, msg, nil)
	if err != nil {
		return nil, err
	}
	return value.Export(), nil
}

// EvalObject runs source and returns the exported object result, used by
// transform scripts and attribute mappings.
func (r *Runtime) EvalObject(source string, msg, metadata map[string]any) (map[string]any, error) {
	value, err := r.eval(source, msg, metadata)
	if err != nil {
		return nil, err
	}
	result, ok := value.Export().(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: result is not an object: %s", ErrScriptFailed, value)
	}
	return result, nil
}

// eval creates an isolated VM, binds the inputs, installs the timeout
// interrupt and executes the source. User snippets use bare return like
// ThingsBoard scripts, so the source is wrapped in an anonymous function
// invoked with (msg, metadata).
func (r *Runtime) eval(source string, msg, metadata map[string]any) (goja.Value, error) {
	vm := goja.New()

	if r.executionTimeout > 0 {
		timer := time.AfterFunc(r.executionTimeout, func() {
			vm.Interrupt("script execution timeout")
		})
		defer timer.Stop()
	}

	if err := vm.Set("msg", msg); err != nil {
		return nil, fmt.Errorf("bind msg: %w", err)
	}
	if err := vm.Set("metadata", metadata); err != nil {
		return nil, fmt.Errorf("bind metadata: %w", err)
	}
	for key, value := range msg {
		if err := vm.Set(key, value); err != nil {
			return nil, fmt.Errorf("bind message field %s: %w", key, err)
		}
	}

	wrapped := fmt.Sprintf(
		"(function(msg, metadata){\n%s\n})(msg, metadata)", source)
	value, err := vm.RunString(wrapped)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrScriptFailed, err)
	}
	return value, nil
}
