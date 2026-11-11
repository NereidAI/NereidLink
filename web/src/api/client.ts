// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// client.ts creates the shared axios instance. Requests are same-origin in
// production and proxied by Vite in development. The response error shape
// follows the global error envelope documented in OpenAPI.

import axios, { AxiosError, type InternalAxiosRequestConfig } from "axios";

import { authStore } from "../store/authStore";

const client = axios.create({
  baseURL: "/",
  timeout: 30000,
});

// Endpoint paths that must never carry a bearer token or trigger a
// refresh retry: failing them redirects to login through the caller.
const publicPaths = [
  "/v1/auth/login",
  "/v1/auth/refresh",
  "/v1/auth/oidc/status",
  "/v1/auth/sso/status",
];

// request interceptor attaches the stored access token to every
// authenticated request.
client.interceptors.request.use((config) => {
  if (publicPaths.some((path) => config.url?.startsWith(path))) {
    return config;
  }
  const token = authStore.getState().accessToken;
  if (token) {
    config.headers.Authorization = `Bearer ${token}`;
  }
  return config;
});

// refreshInFlight deduplicates 401 handling when several requests fail
// at once: exactly one refresh call is made and all wait on the same
// promise.
let refreshInFlight: Promise<string | null> | null = null;

// runRefresh performs the rotation and stores or clears the session.
function runRefresh(): Promise<string | null> {
  if (refreshInFlight) {
    return refreshInFlight;
  }

  const store = authStore.getState();
  if (!store.refreshToken) {
    return Promise.resolve(null);
  }

  refreshInFlight = axios
    .post("/v1/auth/refresh", { refresh_token: store.refreshToken })
    .then((response) => {
      store.setSession(response.data);
      return response.data.access_token as string;
    })
    .catch(() => {
      authStore.getState().clearSession();
      return null;
    })
    .finally(() => {
      refreshInFlight = null;
    });
  return refreshInFlight;
}

// retryConfig marks a request already retried after refresh, so a
// second 401 does not loop.
interface RetryConfig extends InternalAxiosRequestConfig {
  _retriedAfterRefresh?: boolean;
}

// response interceptor transparently refreshes once on 401 and replays
// the original request; failure clears the session so the route guard
// moves the user to login.
client.interceptors.response.use(
  (response) => response,
  async (error: AxiosError) => {
    const config = error.config as RetryConfig | undefined;
    const isPublic = config
      ? publicPaths.some((path) => config.url?.startsWith(path))
      : true;

    if (error.response?.status === 401 && config && !isPublic) {
      // First 401: try one silent refresh-and-replay. No refresh token
      // (or the rotation failed/returned nothing): clear the session so
      // the reactive route guard redirects to login immediately instead
      // of leaving the app shell mounted over failed, empty queries.
      if (!config._retriedAfterRefresh) {
        config._retriedAfterRefresh = true;
        const token = await runRefresh();
        if (token) {
          config.headers.Authorization = `Bearer ${token}`;
          return client(config);
        }
        authStore.getState().clearSession();
      } else {
        // Replayed request is still unauthorized: the new access token
        // is itself rejected (revocation/epoch) — give up and sign out.
        authStore.getState().clearSession();
      }
    }
    return Promise.reject(error);
  },
);

// ApiError normalizes the documented {error:{code,message}} envelope while
// keeping the HTTP status available for callers that need it.
export interface ApiError {
  status: number;
  code: string;
  message: string;
}

// extractError pulls the first usable message from an axios failure.
export function extractError(error: unknown): ApiError {
  if (axios.isAxiosError(error)) {
    const data = error.response?.data as
      | { error?: { code?: string; message?: string }; message?: string }
      | undefined;
    return {
      status: error.response?.status ?? 0,
      code: data?.error?.code ?? "REQUEST_FAILED",
      message: data?.error?.message ?? data?.message ?? error.message,
    };
  }
  return { status: 0, code: "REQUEST_FAILED", message: String(error) };
}

export default client;
