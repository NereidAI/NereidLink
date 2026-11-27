// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Regression loop for the sidebar footer version stamp: the build-time
// VITE_APP_VERSION identity (0.0.0-dev-<short-sha> on dev images, bare SemVer
// on releases) must pass through verbatim, while missing/blank stamps fall
// back to the "dev" label.

import assert from "node:assert/strict";
import { footerVersion } from "../src/utils/version.ts";

assert.equal(
  footerVersion("0.0.0-dev-a1b2c3d"),
  "0.0.0-dev-a1b2c3d",
  "the immutable dev stamp, including the short git sha, must pass through",
);
assert.equal(
  footerVersion("0.0.1"),
  "0.0.1",
  "the release SemVer stamp must pass through",
);
assert.equal(footerVersion(undefined), "dev", "an unset stamp falls back to dev");
assert.equal(footerVersion(""), "dev", "an empty stamp falls back to dev");
assert.equal(footerVersion("   "), "dev", "a whitespace-only stamp falls back to dev");
assert.equal(
  footerVersion("  0.0.0-dev-deadbee  "),
  "0.0.0-dev-deadbee",
  "surrounding whitespace is trimmed but the stamp is preserved",
);

console.log("sidebar footer version checks passed");
