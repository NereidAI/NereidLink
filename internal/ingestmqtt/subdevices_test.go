// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package ingestmqtt

import "testing"

// TestReplaceSubdevicesConvergesSet verifies the post-manifest local
// authorization set is reset to exactly the in-sync list, removing stale
// entries a previous frame attached but the current one omits.
func TestReplaceSubdevicesConvergesSet(t *testing.T) {
	hook := &AuthHook{connections: map[string]connection{
		"c": {subdevices: map[string]struct{}{
			"t.a": {},
			"t.b": {},
			"t.c": {},
		}},
	}}

	// Current manifest reports a and d; b and c are no longer online.
	hook.replaceSubdevices("c", "t", []string{"a", "d"})

	if !hook.isSubdevice("c", "t", "a") {
		t.Fatal("a should remain attached")
	}
	if !hook.isSubdevice("c", "t", "d") {
		t.Fatal("d should be attached after manifest")
	}
	if hook.isSubdevice("c", "t", "b") || hook.isSubdevice("c", "t", "c") {
		t.Fatal("omitted devices must be detached from the local set")
	}
}

// TestReplaceSubdevicesUnknownConnectionIsNoOp ensures a stale publish from a
// connection that already closed cannot create phantom state.
func TestReplaceSubdevicesUnknownConnectionIsNoOp(t *testing.T) {
	hook := &AuthHook{connections: map[string]connection{}}
	hook.replaceSubdevices("gone", "t", []string{"a"})
	if len(hook.connections) != 0 {
		t.Fatal("unknown connection must not be created")
	}
}

// TestReplaceSubdevicesEmptiesSet verifies an empty in-sync list clears all
// locally tracked attachments rather than leaving them behind.
func TestReplaceSubdevicesEmptiesSet(t *testing.T) {
	hook := &AuthHook{connections: map[string]connection{
		"c": {subdevices: map[string]struct{}{"t.a": {}}},
	}}
	hook.replaceSubdevices("c", "t", nil)
	if hook.isSubdevice("c", "t", "a") {
		t.Fatal("empty in-sync list must detach every sub-device")
	}
}
