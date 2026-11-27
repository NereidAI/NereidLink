// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// footerVersion normalizes the build-time VITE_APP_VERSION stamp for the
// sidebar footer. CI images bake an immutable identity (dev channel:
// 0.0.0-dev-<short-sha>, releases: bare SemVer); local builds fall back to the
// package version through the Vite define. Any empty/whitespace stamp renders
// as "dev" so the footer never shows a blank label.
export function footerVersion(raw: string | undefined): string {
  const stamped = raw?.trim();
  return stamped ? stamped : "dev";
}
