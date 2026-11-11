// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// RequireAuth guards authenticated routes: without a stored session it
// redirects to /login, preserving the requested path so login returns
// the user where they started.

import { Navigate, useLocation } from "react-router-dom";
import type { ReactNode } from "react";

import { authStore } from "../store/authStore";

// RequireAuth wraps protected content. It subscribes to the session
// (not a one-shot getState()) so a session cleared by the HTTP layer on a
// hard 401 — expired refresh token, revocation, epoch bump — immediately
// redirects to login instead of leaving the authenticated shell mounted
// over empty, failed queries.
export default function RequireAuth({ children }: { children: ReactNode }) {
  const location = useLocation();
  const accessToken = authStore((s) => s.accessToken);

  if (!accessToken) {
    return (
      <Navigate
        to="/login"
        replace
        state={{ returnTo: location.pathname + location.search }}
      />
    );
  }
  return <>{children}</>;
}
