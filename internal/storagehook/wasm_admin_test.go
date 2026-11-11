// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package storagehook

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
)

type fakeWasmValidator struct{ reject bool }

func (f fakeWasmValidator) Validate(_ context.Context, binary []byte) error {
	if f.reject {
		return errors.New("compile failed")
	}
	return nil
}

// validWasmConfig encodes a config object with a magic-valid module payload
// (decodeWasmModule only checks base64/magic/size here; real compilation is
// covered by the gated wazero tests).
func validWasmConfig() []byte {
	binary := append([]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00},
		make([]byte, 8)...)
	b64 := base64.StdEncoding.EncodeToString(binary)
	return []byte(`{"module_b64":"` + b64 + `"}`)
}

func TestValidateWasmHook(t *testing.T) {
	s := &Store{wasm: DefaultWasmOptions(), wasmValidator: fakeWasmValidator{}}

	t.Run("accepts well-formed wasm hook", func(t *testing.T) {
		input := HookDTO{
			Name:          "w",
			HookType:      TypeWasm,
			Config:        validWasmConfig(),
			TimeoutMS:     1000,
			FailurePolicy: PolicyPassthrough,
		}
		if err := s.validateHook(context.Background(), input); err != nil {
			t.Fatalf("want valid, got %v", err)
		}
	})

	t.Run("requires module", func(t *testing.T) {
		input := HookDTO{Name: "w", HookType: TypeWasm, Config: []byte(`{}`),
			TimeoutMS: 1000, FailurePolicy: PolicyPassthrough}
		if err := s.validateHook(context.Background(), input); err == nil {
			t.Fatal("missing module must be invalid")
		}
	})

	t.Run("surfaces compile failure", func(t *testing.T) {
		s2 := &Store{wasmValidator: fakeWasmValidator{reject: true}}
		input := HookDTO{Name: "w", HookType: TypeWasm, Config: validWasmConfig(),
			TimeoutMS: 1000, FailurePolicy: PolicyPassthrough}
		if err := s2.validateHook(context.Background(), input); err == nil {
			t.Fatal("validator rejection must invalidate the hook")
		}
	})
}
