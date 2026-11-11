#!/usr/bin/env bash
# Copyright (C) 2026 YIQISOFT
# SPDX-License-Identifier: Apache-2.0
#
# post-deploy.sh initializes only missing first-run state after a Helm install.
# It bootstraps a strong local admin through the public one-time API and installs
# a vendor-signed deployment license through the host CLI. Existing credentials
# and licenses are never overwritten by an idempotent deployment rerun.
set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
DEPLOYMENT_ROOT="${DEPLOYMENT_ROOT:-${REPO_ROOT}/../.deployment}"
RELEASE="${RELEASE:-nereidlink}"
NAMESPACE="${NAMESPACE:-nereidlink}"
STATE_DIR="${DEPLOY_STATE_DIR:-${DEPLOYMENT_ROOT}/deploy-values/nereidlink}"
LOG_DIR="${DEPLOY_LOG_DIR:-${DEPLOYMENT_ROOT}/deploy-logs/nereidlink}"
LOCAL_HTTP_PORT="${LOCAL_HTTP_PORT:-18081}"
LOCAL_PG_PORT="${LOCAL_PG_PORT:-15432}"
POSTGRES_SERVICE="${POSTGRES_SERVICE:-${RELEASE}-postgresql}"
PUBLIC_KEY="${LICENSE_PUBLIC_KEY:-${REPO_ROOT}/configs/license/public.pem}"
PRIVATE_KEY="${LICENSE_PRIVATE_KEY:-${STATE_DIR}/license/private/vendor-ed25519.pem}"
LICENSE_CUSTOMER="${LICENSE_CUSTOMER:-YIQISOFT Internal}"
LICENSE_YEARS="${LICENSE_YEARS:-10}"
CREDENTIAL_FILE="${STATE_DIR}/admin-credentials.env"
LICENSE_FILE="${STATE_DIR}/nereidlink-deployment.license.json"

if kubectl auth can-i get nodes >/dev/null 2>&1; then
  KUBECTL="${KUBECTL:-kubectl}"
else
  KUBECTL="${KUBECTL:-sudo -n kubectl}"
fi

mkdir -p "${STATE_DIR}" "${LOG_DIR}" "${STATE_DIR}/license/private"
chmod 700 "${STATE_DIR}" "${LOG_DIR}" "${STATE_DIR}/license" "${STATE_DIR}/license/private"
for sensitive_file in \
  "${STATE_DIR}/values.yaml" "${CREDENTIAL_FILE}" "${LICENSE_FILE}" "${PRIVATE_KEY}"; do
  if [[ -f "${sensitive_file}" ]]; then
    chmod 600 "${sensitive_file}"
  fi
done
WORK_DIR="$(mktemp -d "${STATE_DIR}/work.XXXXXX")"
HTTP_PF_PID=""
PG_PF_PID=""

# cleanup releases both local forwarding sockets and removes the temporary copy
# of the live Secret configuration. Persistent hand-off files stay in STATE_DIR.
cleanup() {
  if [[ -n "${PG_PF_PID}" ]]; then
    kill "${PG_PF_PID}" 2>/dev/null || true
  fi
  if [[ -n "${HTTP_PF_PID}" ]]; then
    kill "${HTTP_PF_PID}" 2>/dev/null || true
  fi
  rm -rf "${WORK_DIR}"
}
trap cleanup EXIT

# kube runs kubectl through the same sudo-aware command selected by deploy.sh.
# KUBECTL intentionally undergoes shell word splitting to support "sudo -n
# kubectl"; all actual kubectl arguments remain quoted by the caller.
kube() {
  ${KUBECTL} "$@"
}

for command in kubectl curl openssl go; do
  command -v "${command}" >/dev/null 2>&1 || {
    echo "required command not found: ${command}" >&2
    exit 1
  }
done
[[ -f "${PUBLIC_KEY}" ]] || { echo "license public key not found: ${PUBLIC_KEY}" >&2; exit 1; }

echo ">> wait for ${RELEASE} application rollout"
kube -n "${NAMESPACE}" rollout status deployment/"${RELEASE}" --timeout=240s
APP_POD="$(kube -n "${NAMESPACE}" get pod -l app.kubernetes.io/component=app \
  -o jsonpath='{.items[0].metadata.name}')"
[[ -n "${APP_POD}" ]] || { echo "no NereidLink application pod found" >&2; exit 1; }

kube -n "${NAMESPACE}" port-forward pod/"${APP_POD}" \
  "${LOCAL_HTTP_PORT}:8080" >"${WORK_DIR}/http-port-forward.log" 2>&1 &
HTTP_PF_PID=$!
sleep 3
HTTP_BASE="http://127.0.0.1:${LOCAL_HTTP_PORT}"
BOOTSTRAP_STATUS="$(curl --noproxy '*' --retry 10 --retry-connrefused --retry-delay 1 \
  -fsS "${HTTP_BASE}/v1/auth/bootstrap-status")"

if grep -q '"required":true' <<<"${BOOTSTRAP_STATUS}"; then
  ADMIN_PASSWORD="${NL_ADMIN_PASSWORD:-$(openssl rand -hex 24)}"
  BOOTSTRAP_RESPONSE="$(printf '{"password":"%s"}' "${ADMIN_PASSWORD}" | \
    curl --noproxy '*' -fsS -X POST "${HTTP_BASE}/v1/auth/bootstrap" \
      -H 'Content-Type: application/json' --data-binary @-)"
  grep -q '"access_token"' <<<"${BOOTSTRAP_RESPONSE}" || {
    echo "admin bootstrap returned no access token" >&2
    exit 1
  }
  printf '%s\n' \
    '# NereidLink built-in platform admin; save securely and never commit.' \
    'LINK_ADMIN_EMAIL=admin@nereidai.local' \
    "LINK_ADMIN_PASSWORD=${ADMIN_PASSWORD}" >"${CREDENTIAL_FILE}"
  chmod 600 "${CREDENTIAL_FILE}"
  echo ">> initialized built-in admin with a strong random password"
else
  echo ">> built-in admin is already initialized; password left unchanged"
fi

# The application image is distroless, so license CLI commands run from a
# freshly built host binary against a temporary localhost PostgreSQL forward.
# Only the DSN endpoint and host-local key/clock paths differ from live config.
mkdir -p "${WORK_DIR}/config.d"
kube -n "${NAMESPACE}" get configmap "${RELEASE}-config" \
  -o jsonpath='{.data.10-base\.yaml}' >"${WORK_DIR}/config.d/10-base.yaml"
kube -n "${NAMESPACE}" get secret "${RELEASE}-config-secret" \
  -o jsonpath='{.data.90-secret\.yaml}' | base64 -d >"${WORK_DIR}/config.d/90-secret.yaml"
sed -i -E "/^[[:space:]]*dsn:/ s#@[^/]+/#@127.0.0.1:${LOCAL_PG_PORT}/#" \
  "${WORK_DIR}/config.d/90-secret.yaml"
sed -i -E "s#^([[:space:]]*public_key_path:).*#\\1 \"${PUBLIC_KEY}\"#; \
  s#^([[:space:]]*clock_file:).*#\\1 \"${WORK_DIR}/license-clock\"#" \
  "${WORK_DIR}/config.d/10-base.yaml"

(
  cd "${REPO_ROOT}"
  go build -trimpath -o "${WORK_DIR}/nereidlink" ./cmd/nereidlink
)

kube -n "${NAMESPACE}" port-forward service/"${POSTGRES_SERVICE}" \
  "${LOCAL_PG_PORT}:5432" >"${WORK_DIR}/postgres-port-forward.log" 2>&1 &
PG_PF_PID=$!
sleep 3
LICENSE_STATUS=""
for attempt in {1..10}; do
  if LICENSE_STATUS="$("${WORK_DIR}/nereidlink" -c "${WORK_DIR}/config.d" license status 2>"${WORK_DIR}/license-status.err")"; then
    break
  fi
  sleep 2
done
[[ -n "${LICENSE_STATUS}" ]] || {
  cat "${WORK_DIR}/license-status.err" >&2
  echo "could not read deployment license status" >&2
  exit 1
}

if grep -q '"installed":true' <<<"${LICENSE_STATUS}"; then
  echo ">> deployment license is already installed; license left unchanged"
else
  [[ -f "${PRIVATE_KEY}" ]] || {
    echo "license signing key not found: ${PRIVATE_KEY}" >&2
    echo "keep the vendor private key offline and set LICENSE_PRIVATE_KEY" >&2
    exit 1
  }
  (
    cd "${REPO_ROOT}"
    go build -trimpath -o "${WORK_DIR}/license-sign" ./tools/license-sign
  )
  "${WORK_DIR}/license-sign" --key "${PRIVATE_KEY}" --scope '*' \
    --customer "${LICENSE_CUSTOMER}" --years "${LICENSE_YEARS}" --out "${LICENSE_FILE}"
  chmod 600 "${LICENSE_FILE}"
  "${WORK_DIR}/nereidlink" -c "${WORK_DIR}/config.d" license install --file "${LICENSE_FILE}"
  LICENSE_STATUS="$("${WORK_DIR}/nereidlink" -c "${WORK_DIR}/config.d" license status)"
  grep -q '"installed":true' <<<"${LICENSE_STATUS}" || {
    echo "license installation did not reach installed state" >&2
    exit 1
  }
  echo ">> generated and installed the default deployment license"
fi
printf '%s\n' "${LICENSE_STATUS}" >"${STATE_DIR}/license-status.json"
chmod 600 "${STATE_DIR}/license-status.json"

echo ""
echo "================ SAVE THE DEPLOYMENT HAND-OFF ================"
if [[ -f "${CREDENTIAL_FILE}" ]]; then
  echo "Admin credentials (mode 600): ${CREDENTIAL_FILE}"
else
  echo "Admin was already initialized; no credential was generated by this run."
fi
if [[ -f "${LICENSE_FILE}" ]]; then
  echo "Signed deployment license (mode 600): ${LICENSE_FILE}"
fi
echo "License status: ${STATE_DIR}/license-status.json"
echo "Deploy logs: ${LOG_DIR}"
echo "=============================================================="
