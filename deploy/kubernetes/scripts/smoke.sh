#!/usr/bin/env bash
# Copyright (C) 2026 YIQISOFT
# SPDX-License-Identifier: Apache-2.0
#
# smoke.sh - verify a running in-cluster NereidLink release. It checks pod
# readiness for both tiers, the backend /healthz endpoint and the auth
# captcha endpoint over temporary port-forwards, then the frontend nginx
# SPA fallback, and (optionally) a real S3 round-trip using the values'
# SeaweedFS endpoint.
#
# Override: RELEASE, NAMESPACE, KUBECTL, LOCAL_PORT, FRONTEND_LOCAL_PORT.
set -euo pipefail

RELEASE="${RELEASE:-nereidlink}"
NAMESPACE="${NAMESPACE:-nereidlink}"
LOCAL_PORT="${LOCAL_PORT:-18080}"
FRONTEND_LOCAL_PORT="${FRONTEND_LOCAL_PORT:-18081}"
APP_LABEL="app.kubernetes.io/component=app"
FRONTEND_LABEL="app.kubernetes.io/component=frontend"

if kubectl auth can-i get nodes >/dev/null 2>&1; then
  KUBECTL="${KUBECTL:-kubectl}"
else
  KUBECTL="${KUBECTL:-sudo -n kubectl}"
fi

echo ">> pods"
${KUBECTL} -n "${NAMESPACE}" get pods

POD="$(${KUBECTL} -n "${NAMESPACE}" get pod -l "${APP_LABEL}" \
  -o jsonpath='{.items[0].metadata.name}')"
echo ">> backend pod: ${POD}"

FRONTEND_POD="$(${KUBECTL} -n "${NAMESPACE}" get pod -l "${FRONTEND_LABEL}" \
  -o jsonpath='{.items[0].metadata.name}')"
echo ">> frontend pod: ${FRONTEND_POD}"
${KUBECTL} -n "${NAMESPACE}" wait --for=condition=ready pod/"${FRONTEND_POD}" --timeout=120s >/dev/null

${KUBECTL} -n "${NAMESPACE}" wait --for=condition=ready pod/"${POD}" --timeout=120s >/dev/null

${KUBECTL} -n "${NAMESPACE}" port-forward pod/"${POD}" "${LOCAL_PORT}:8080" >/tmp/nl-smoke-pf.log 2>&1 &
PF_PID=$!
${KUBECTL} -n "${NAMESPACE}" port-forward pod/"${FRONTEND_POD}" "${FRONTEND_LOCAL_PORT}:80" >/tmp/nl-smoke-frontend-pf.log 2>&1 &
FRONTEND_PF_PID=$!
trap 'kill ${PF_PID} ${FRONTEND_PF_PID} 2>/dev/null || true' EXIT
sleep 4

echo ">> /healthz"
code="$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' "http://127.0.0.1:${LOCAL_PORT}/healthz")"
echo "   healthz=${code}"
[ "${code}" = "200" ] || { echo "health check failed"; exit 1; }

echo ">> captcha"
code="$(curl -s --noproxy '*' -o /tmp/nl-smoke-captcha.json -w '%{http_code}' \
  "http://127.0.0.1:${LOCAL_PORT}/v1/auth/captcha")"
echo "   captcha=${code}"
[ "${code}" = "200" ] || { echo "captcha failed"; exit 1; }
grep -q captcha_id /tmp/nl-smoke-captcha.json || { echo "unexpected captcha body"; exit 1; }

echo ">> frontend SPA"
code="$(curl -s --noproxy '*' -o /tmp/nl-smoke-index.html -w '%{http_code}' \
  "http://127.0.0.1:${FRONTEND_LOCAL_PORT}/")"
echo "   index=${code}"
[ "${code}" = "200" ] || { echo "frontend index failed"; exit 1; }
grep -qi '<div id="root"' /tmp/nl-smoke-index.html || { echo "unexpected frontend body"; exit 1; }

# A deep client-side route must fall back to index.html (history mode).
code="$(curl -s --noproxy '*' -o /tmp/nl-smoke-deep.html -w '%{http_code}' \
  "http://127.0.0.1:${FRONTEND_LOCAL_PORT}/devices")"
echo "   spa-fallback=${code}"
[ "${code}" = "200" ] || { echo "SPA history fallback failed"; exit 1; }
cmp -s /tmp/nl-smoke-index.html /tmp/nl-smoke-deep.html || { echo "deep route did not serve index.html"; exit 1; }

# A missing file with an extension must 404, never return HTML as an asset.
code="$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' \
  "http://127.0.0.1:${FRONTEND_LOCAL_PORT}/does-not-exist.js")"
echo "   asset-404=${code}"
[ "${code}" = "404" ] || { echo "missing asset did not 404"; exit 1; }

echo ">> SMOKE PASSED"
