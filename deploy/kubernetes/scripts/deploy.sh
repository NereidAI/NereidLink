#!/usr/bin/env bash
# Copyright (C) 2026 YIQISOFT
# SPDX-License-Identifier: Apache-2.0
#
# deploy.sh - render/install/upgrade the NereidLink Helm release into K3s.
# Usage:
#   deploy/kubernetes/scripts/deploy.sh [install|upgrade|post-deploy|template|diff]
#
# Requires kubectl + helm on PATH and cluster access (sudo is used when the
# current user cannot read /etc/rancher/k3s/k3s.yaml). Override:
#   KUBECTL, HELM, RELEASE, NAMESPACE, CHART, VALUES, DEPLOYMENT_ROOT,
#   DEPLOY_STATE_DIR, DEPLOY_LOG_DIR
set -euo pipefail
umask 077

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
DEPLOYMENT_ROOT="${DEPLOYMENT_ROOT:-${REPO_ROOT}/../.deployment}"
CHART="${CHART:-${SCRIPT_DIR}/../helm/nereidlink}"
RELEASE="${RELEASE:-nereidlink}"
NAMESPACE="${NAMESPACE:-nereidlink}"
ACTION="${1:-upgrade}"
STATE_DIR="${DEPLOY_STATE_DIR:-${DEPLOYMENT_ROOT}/deploy-values/nereidlink}"
LOG_DIR="${DEPLOY_LOG_DIR:-${DEPLOYMENT_ROOT}/deploy-logs/nereidlink}"
HELM_VALUES=()
if [[ -n "${VALUES:-}" ]]; then
  HELM_VALUES=(-f "${VALUES}")
elif [[ -f "${STATE_DIR}/values.yaml" ]]; then
  HELM_VALUES=(-f "${STATE_DIR}/values.yaml")
fi
if [[ -f "${STATE_DIR}/values.yaml" ]]; then
  chmod 700 "${STATE_DIR}"
  chmod 600 "${STATE_DIR}/values.yaml"
fi

if sudo -n kubectl auth can-i get nodes >/dev/null 2>&1 && ! kubectl auth can-i get nodes >/dev/null 2>&1; then
  KUBECTL="${KUBECTL:-sudo -n kubectl}"
  HELM="${HELM:-sudo -n helm}"
else
  KUBECTL="${KUBECTL:-kubectl}"
  HELM="${HELM:-helm}"
fi

# run_post_deploy keeps environment selection in this entry point while the
# separate script owns first-run credential and license initialization.
run_post_deploy() {
  KUBECTL="${KUBECTL}" RELEASE="${RELEASE}" NAMESPACE="${NAMESPACE}" \
    DEPLOY_STATE_DIR="${STATE_DIR}" DEPLOY_LOG_DIR="${LOG_DIR}" \
    "${SCRIPT_DIR}/post-deploy.sh"
}

# Mirror deployment and first-run output into a mode-0600 log. Configuration,
# credentials and licenses stay in STATE_DIR, separately from deployment logs.
case "${ACTION}" in
  install|upgrade|post-deploy)
    mkdir -p "${STATE_DIR}" "${LOG_DIR}"
    chmod 700 "${STATE_DIR}" "${LOG_DIR}"
    NL_DEPLOY_LOG_FILE="${LOG_DIR}/$(date +%Y%m%d-%H%M%S)-${ACTION}.log"
    (umask 077; touch "${NL_DEPLOY_LOG_FILE}")
    chmod 600 "${NL_DEPLOY_LOG_FILE}"
    ln -sfn "$(basename "${NL_DEPLOY_LOG_FILE}")" "${LOG_DIR}/latest-${ACTION}.log"
    export NL_DEPLOY_LOG_FILE
    exec > >(tee -a "${NL_DEPLOY_LOG_FILE}") 2>&1
    echo ">> deploy log: ${NL_DEPLOY_LOG_FILE}"
    ;;
esac

case "${ACTION}" in
  template)
    ${HELM} template "${RELEASE}" "${CHART}" -n "${NAMESPACE}" "${HELM_VALUES[@]}"
    ;;
  diff)
    ${HELM} diff upgrade "${RELEASE}" "${CHART}" -n "${NAMESPACE}" "${HELM_VALUES[@]}" || true
    ;;
  install)
    ${KUBECTL} create namespace "${NAMESPACE}" --dry-run=client -o yaml | ${KUBECTL} apply -f -
    ${HELM} install "${RELEASE}" "${CHART}" -n "${NAMESPACE}" "${HELM_VALUES[@]}"
    run_post_deploy
    ;;
  upgrade)
    ${KUBECTL} create namespace "${NAMESPACE}" --dry-run=client -o yaml | ${KUBECTL} apply -f -
    ${HELM} upgrade --install "${RELEASE}" "${CHART}" -n "${NAMESPACE}" "${HELM_VALUES[@]}"
    run_post_deploy
    ;;
  post-deploy)
    run_post_deploy
    ;;
  uninstall)
    ${HELM} uninstall "${RELEASE}" -n "${NAMESPACE}"
    ;;
  status)
    ${HELM} status "${RELEASE}" -n "${NAMESPACE}"
    ${KUBECTL} -n "${NAMESPACE}" get pods,svc,ingress
    ;;
  *)
    echo "usage: $0 [install|upgrade|post-deploy|template|diff|uninstall|status]" >&2
    exit 2
    ;;
esac
