// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"crypto/rand"
	"encoding/json"
	"sort"
	"testing"
)

func TestCodecRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	c, err := NewCodec(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"", "p@ssw0rd", "very-long-secret-value-with-unicode-密钥"} {
		sealed, err := c.Seal([]byte(plain))
		if err != nil {
			t.Fatalf("seal: %v", err)
		}
		if sealed == plain {
			t.Fatal("sealed value must not equal plaintext")
		}
		opened, err := c.Open(sealed)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if string(opened) != plain {
			t.Fatalf("round trip mismatch: got %q want %q", opened, plain)
		}
	}
	// Two seals of the same value differ (random nonce).
	s1, _ := c.Seal([]byte("same"))
	s2, _ := c.Seal([]byte("same"))
	if s1 == s2 {
		t.Fatal("expected distinct ciphertexts from random nonces")
	}
	// Tamper fails authentication.
	sealed, _ := c.Seal([]byte("x"))
	tampered := sealed[:len(sealed)-2] + "AA"
	if _, err := c.Open(tampered); err == nil {
		t.Fatal("tampered blob must fail to open")
	}
	// Wrong key fails.
	other := make([]byte, 32)
	other[0] = 1
	c2, _ := NewCodec(other)
	if _, err := c2.Open(sealed); err == nil {
		t.Fatal("opening under another KEK must fail")
	}
	if _, err := NewCodec(make([]byte, 31)); err == nil {
		t.Fatal("31-byte KEK must be rejected")
	}
}

func TestParseReference(t *testing.T) {
	cases := map[string]struct {
		name string
		ok   bool
	}{
		"secret://modbus-token": {"modbus-token", true},
		"secret://a.b_c-1":      {"a.b_c-1", true},
		"plain":                 {"", false},
		"secret://":             {"", false},
		"secret://bad name":     {"", false},
		"secret://-lead":        {"", false},
	}
	for in, want := range cases {
		name, ok := ParseReference(in)
		if ok != want.ok || name != want.name {
			t.Errorf("ParseReference(%q)=%q,%v want %q,%v", in, name, ok, want.name, want.ok)
		}
	}
}

func TestResolveDocument(t *testing.T) {
	doc := json.RawMessage(`{
		"host":"10.0.0.1",
		"token":"secret://tok",
		"nested":{"pw":"secret://pw","keep":"plain"},
		"list":["secret://tok",{"x":"secret://missing"}],
		"num":42}`)
	out, missing := ResolveDocument(doc, map[string]string{"tok": "T", "pw": "P"})
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("resolved not JSON: %v (%s)", err, out)
	}
	if got["token"] != "T" {
		t.Fatalf("token = %v", got["token"])
	}
	nested := got["nested"].(map[string]any)
	if nested["pw"] != "P" || nested["keep"] != "plain" {
		t.Fatalf("nested = %v", nested)
	}
	list := got["list"].([]any)
	if list[0] != "T" {
		t.Fatalf("list[0] = %v", list[0])
	}
	if list[1].(map[string]any)["x"] != "" {
		t.Fatal("missing secret must resolve to empty string")
	}
	if got["num"].(float64) != 42 {
		t.Fatal("non-string values must be preserved")
	}
	sort.Strings(missing)
	if len(missing) != 1 || missing[0] != "missing" {
		t.Fatalf("missing = %v", missing)
	}

	names := FindReferences(doc)
	sort.Strings(names)
	if len(names) != 3 || names[0] != "missing" || names[1] != "pw" || names[2] != "tok" {
		t.Fatalf("FindReferences = %v", names)
	}
}
