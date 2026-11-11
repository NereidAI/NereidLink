// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// authStore.ts holds the authenticated session: access/refresh tokens
// and the user profile, persisted to localStorage so a reload keeps the
// session until the refresh token expires. The axios layer reads
// getState() directly; UI components subscribe with hooks.

import { create } from "zustand";
import { persist } from "zustand/middleware";

import type { AuthUser, TokenPair } from "../api/auth";

// AuthState is the session store contract.
interface AuthState {
  // accessToken is the bearer token, null when signed out.
  accessToken: string | null;

  // refreshToken rotates the access token, null when signed out.
  refreshToken: string | null;

  // user is the authenticated profile.
  user: AuthUser | null;

  // setSession stores a freshly issued token pair.
  setSession: (pair: TokenPair) => void;

  // clearSession drops every credential on logout or expiry.
  clearSession: () => void;
}

// authStore is the singleton session store.
export const authStore = create<AuthState>()(
  persist(
    (set) => ({
      accessToken: null,
      refreshToken: null,
      user: null,
      setSession: (pair) =>
        set({
          accessToken: pair.access_token,
          refreshToken: pair.refresh_token,
          user: pair.user,
        }),
      clearSession: () =>
        set({ accessToken: null, refreshToken: null, user: null }),
    }),
    { name: "nl:auth" },
  ),
);
