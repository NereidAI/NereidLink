<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# NereidLink deployment

NereidLink ships as one self-contained static binary (HTTP/WS API, MQTT
ingress, registry, NATS/Postgres clients, and an embedded console for
standalone use) but is packaged and deployed as a split two-image topology:
the `nereidlink-backend` Go process and the `nereidlink-frontend` static
nginx console, with path-based splitting at the edge/Ingress. It supports
two deployment topologies:

| Mode | Location | Brings its own Postgres/NATS/S3? |
|------|----------|----------------------------------|
| Single host (split containers) | `deploy/docker-compose/` | Yes — one `docker compose up` |
| Kubernetes / K3s        | `deploy/kubernetes/`     | Bundled Postgres + NATS; S3 reused or bring-your-own |

All images use public canonical coordinates (`ghcr.io/nereidai/...` for
suite images and the upstream registries for dependencies). Air-gapped
clusters resolve them through the node-level containerd registry mirror
configured by NereidOverview's `harbor-sync.sh` (treaty §18); no internal
hostname appears in committed files.

## Directory layout

```
deploy/
  images/
    backend/Dockerfile      # Go backend image (build context = repo root)
    frontend/Dockerfile     # node build + static nginx console image
    frontend/nginx.conf     # SPA serving rules; no proxy_pass
    postgres/Dockerfile     # documents the TimescaleDB-enabled Postgres image
  kubernetes/
    helm/nereidlink/        # Helm chart for K3s/Kubernetes
      examples/byo-infra.yaml   # use your own PG/NATS/S3
    scripts/
      deploy.sh             # Helm + first-run admin/license + central deploy logs
      post-deploy.sh        # idempotent admin bootstrap and license installation
      smoke.sh              # readiness + /healthz + captcha checks
  docker-compose/           # single-host split stack (see its README)
```

## Requirements

* PostgreSQL **with the TimescaleDB extension** — migration `0001` runs
  `CREATE EXTENSION timescaledb`. The bundled image is the public
  `timescale/timescaledb:2.17.2-pg16`; air-gapped nodes resolve it through
  the containerd registry mirror (treaty §18). A plain `postgres` image
  fails the first migration.
  The bundled images ship **TimescaleDB Community Edition** under the
  Timescale License (TSL), because migrations `0024`/`0036` use continuous
  aggregates, compression and retention policies that the Apache-2 edition
  does not provide. TSL is free for internal use and for embedding in a
  value-added product like NereidLink, but it prohibits offering TimescaleDB
  itself as a hosted database service. See the root `THIRD_PARTY_NOTICES`
  for the exact terms and redistribution obligations.
* NATS with **JetStream** — streams are auto-created on startup.
* An S3-compatible object store is optional but required for platform-hosted
  firmware upload (ADR-0006). External-URL OTA works without it.

## Image sources and staging

Images use public canonical coordinates only (treaty §18): the two split
suite images `ghcr.io/nereidai/nereidlink-backend:<VERSION>` and
`ghcr.io/nereidai/nereidlink-frontend:<VERSION>`, plus upstream registries
for every dependency. The tag-triggered GitHub Actions workflow is the sole
GHCR publication path and applies the three-tag scheme (§19.2) to each
image: `VERSION`, `VERSION-<utc-timestamp>` and `VERSION-<git-sha>`, with no
`latest` tag. Local `make image` (backend) and `make image-frontend`
(frontend) only build/load the same three tags into the local Docker daemon
and never push.

Air-gapped clusters resolve the public coordinates through the node-level
containerd registry mirror; mirroring the images into the local hub is owned
by NereidOverview's `deploy/scripts/harbor-sync.sh` (root treaty §18.2),
not by any script in this repository.

## Kubernetes / K3s

```bash
# Install/upgrade (creates the namespace and bundled PG + NATS). On success,
# this also initializes missing admin/license state without replacing either
# on subsequent runs. Go, curl, kubectl and the offline vendor key are required.
deploy/kubernetes/scripts/deploy.sh upgrade

# Re-run only the idempotent first-run hand-off when necessary.
deploy/kubernetes/scripts/deploy.sh post-deploy

# Verify
deploy/kubernetes/scripts/smoke.sh
deploy/kubernetes/scripts/deploy.sh status
```

Deployment configuration, generated credentials, the signed license and the
final license status live outside the repository in mode-0700
`../.deployment/deploy-values/nereidlink/`. Deployment logs are separate in
mode-0700 `../.deployment/deploy-logs/nereidlink/`; logs and sensitive files are
mode 0600. Set `DEPLOYMENT_ROOT` to relocate both trees, or `DEPLOY_STATE_DIR`
and `DEPLOY_LOG_DIR` to override them independently.

For Helm `install`, `upgrade`, `template` and `diff`, set `VALUES` to an arbitrary
operator values file. Without `VALUES`, the script uses
`../.deployment/deploy-values/nereidlink/values.yaml` only when that file exists;
otherwise it passes no values file and preserves the independent chart-default
installation. `LICENSE_PRIVATE_KEY` selects the offline signing key, and
`LICENSE_CUSTOMER` / `LICENSE_YEARS` set the default license terms. The CLI prints
hand-off paths at completion so the operator can save them securely. An
administrator can read the generated credential without inspecting cluster
Secrets or deployment logs:

```bash
cat ../.deployment/deploy-values/nereidlink/admin-credentials.env
```

Keep the offline vendor signing key at
`../.deployment/deploy-values/nereidlink/license/private/vendor-ed25519.pem`
(mode 0600, with mode-0700 parent directories) in an encrypted administrator
backup, never in deployment logs or Git. Runtime Secrets and the PKI/KEK
persistent volume are for the application, not a substitute for that encrypted
backup.

The chart runs `nereidlink pki ensure` then `nereidlink migrate up` as init
containers. The device PKI material **and** the tenant-secret AES KEK are
stored on a persistent volume (`/var/lib/nereidlink`); do not delete that PVC
or previously sealed secrets become undecryptable.

### Storage note

* PostgreSQL uses `local-path` (node-local block storage). Postgres requires
  PGDATA mode `0700`, which the NFS CSI share forbids under root-squash. The
  bundled single replica is therefore pinned to one node.
* NATS JetStream and the app data PVC use `nfs-csi`, so they are reachable
  from any node.

### Bring your own infrastructure

If you already run PostgreSQL (TimescaleDB), NATS (JetStream) and S3, disable
the bundled dependencies and supply endpoints/credentials:

```bash
helm install nereidlink deploy/kubernetes/helm/nereidlink \
  -n nereidlink --create-namespace \
  -f deploy/kubernetes/helm/nereidlink/examples/byo-infra.yaml
```

Set `postgresql.mode=external` + `postgresql.externalDSN`,
`nats.enabled=false` + `nats.externalURL`, and point `blob.*` at your own
S3/MinIO/SeaweedFS (or keep the local directory backend on the persistent
volume). The chart fails fast if an external dependency is selected but its
endpoint is left empty.

### High availability

The defaults stay single-node (R1). For a multi-replica topology (2 app
pods, CloudNativePG PostgreSQL, 3-node NATS JetStream, NFS RWX shared
state, NATS-KV leader election), install with
`-f deploy/kubernetes/helm/nereidlink/examples/ha.yaml`. It requires the
CloudNativePG operator and an RWX StorageClass (`nfs-csi` in this
cluster); see the comment header in that file for prerequisites and
cutover notes.

### Public access / DNS

The Ingress (`traefik`, TLS via the `internal-ca` cert-manager cluster issuer)
uses the reserved placeholder `nereidlink.example.com` by default. Override
`ingress.host` with your public DNS name and add its DNS record (the cluster
wildcard is not an automatic wildcard). It splits traffic by path: `/v1`, the
configured `wsPath`
(`/ws`), `/api`, `/metrics`, `/healthz` and — only when `sso.enabled` — the
exact `sso.callbackPath` (default `/auth/ns-callback`, `Exact` match) go to
the backend Service, while `/` goes to the stateless `frontend`
Deployment/Service (nginx, default 2 replicas, no PVC, no arch pin). All
other `/auth/*` URLs, including the SPA route `/auth/callback` that the
backend redirects to after a successful SSO ticket exchange, stay on the
frontend. The frontend image never proxies to the backend; every new
non-`/v1` backend HTTP route must be added to the Ingress path table in the
same change.

Raw MQTT is not HTTP and therefore cannot use the HTTP Ingress. The chart
creates a separate **NodePort** service `nereidlink-mqtt`:

| Listener | Port (in-cluster) | NodePort (every node IP) |
|----------|-------------------|---------------------------|
| MQTT plaintext | 1883 | 31883 |
| MQTTS (TLS, server cert from the PKI volume) | 8883 | 31884 |

Connect a device to `mqtt://<node-ip>:31883` or
`mqtts://<node-ip>:31884`. The MQTTS certificate is re-signed on every start
(`pki server`) so its SANs track `pki.serverHosts` without rotating the device
CA; add every public hostname/IP a device uses to that list. The CA used to
verify device clients is the platform's built-in device CA on the persistent
volume. Toggle/disable this with `mqtt.enabled`, `mqtt.tls.enabled` and
`mqtt.type`.

## First login (no shipped default password)

A fresh chart deployment creates `admin@nereidai.local` with an **empty
password**. The standard `deploy.sh install|upgrade` path completes the
one-time bootstrap API flow with a random 48-hex password and writes it to
`../.deployment/deploy-values/nereidlink/admin-credentials.env` (mode 0600). An
existing admin password is never reset by a deployment rerun; save the credential
file before removing the central deployment-values directory.

For a raw `helm install` without the post-deploy script, open the console
(`https://nereidlink.example.com/` or the node port). The SPA detects first-run
state via `GET /v1/auth/bootstrap-status` and shows a setup screen where the
first visitor sets a strong administrator password exactly once. There is no
shipped default password. (The developer `nereidlink seed` command still
provisions the deterministic dev password `Admin12345!` for local/E2E use.)

## Single sign-on

NereidLink supports two mutually exclusive SSO modes (ADR-0021):

### 1. External OIDC (oidc:)

The product acts as an OIDC relying party against one IdP (Keycloak,
Authing, Entra ID, …). The browser is redirected to the IdP for
authorization-code + PKCE login, and the product exchanges the code,
verifies the id token, JIT-provisions the user, and mints a local session.

```yaml
oidc:
  enabled: true
  issuer: "https://sso.example.com/realms/example"
  clientID: "nereidlink"
  clientSecret: "REPLACE_WITH_SECRET"
  redirectURL: "https://nereidlink.example.com/v1/auth/oidc/callback"
  frontendBaseURL: "https://nereidlink.example.com"
  tenantID: "00000000-0000-0000-0000-000000000001"
  defaultRole: "READ_ONLY"
  groupsClaim: "groups"
  groupRole:
    /nereidlink/admins: TENANT_ADMIN
```

### 2. NereidSphere (NS) one-time handoff ticket (sso:)

The suite portal (NereidSphere) owns the identity plane. NS mints a
30-second single-use ticket after the user logs in, redirects the browser
to the product's `callbackPath` with `?ticket=<id>&to=<path>`, and the
product exchanges it server-to-server at `POST {NS}/api/v1/auth/ticket/verify`
using its shared secret. This avoids the product needing its own OIDC
client registration.

```yaml
sso:
  enabled: true
  nsBaseURL: "https://nereidsphere.example.com"
  # Must equal the product id registered in NS (ticket audience).
  clientID: "link"
  # Must equal the product sso_secret registered in NS.
  ssoSecret: "REPLACE_WITH_PRODUCT_SSO_SECRET"
  # Must equal the product sso_callback_path registered in NS.
  callbackPath: "/auth/ns-callback"
  frontendBaseURL: "https://nereidlink.example.com"
  tenantID: "00000000-0000-0000-0000-000000000001"
  defaultRole: "READ_ONLY"
  # Ticket identity.roles (Keycloak realm roles) -> built-in roles; used
  # only when the ticket carries no /nereidlink group assignments.
  roleMap:
    admin: TENANT_ADMIN
  # /nereidlink/{tenant_uuid}/{segment} group segments -> built-in roles.
  # Each assignment materialises as a local tenant membership on login.
  groupRole:
    admins: TENANT_ADMIN
    engineers: ENGINEER
    read_only: READ_ONLY
  # Revoke memberships removed upstream on the next group-based login
  # (CUSTOMER_USER memberships and platform admins are never touched).
  reconcileMemberships: true
  # Internal CA that signs the NS ingress certificate (Secret in this ns).
  caBundleSecret: "nereid-internal-ca-tls"
  caBundleKey: "ca.crt"

oidc:
  enabled: false
```

The NS suite admin console renders its tenant picker through
`GET /v1/integration/ns/tenants` (NS proxies `/p/link/...`): NS stamps
`X-NS-Integration-Secret` (the same `ssoSecret`) on that path and
forwards the suite roles in `X-User-Roles`; NL requires the secret
(constant-time compare, 401 otherwise) and the suite `admin` role (403).

**Mutually exclusive:** only one of `oidc.enabled` or `sso.enabled` may be true.

## License

A fresh chart deployment has **no license row**, so it starts read-only
(tenant and device creation, and new device connections, are blocked;
existing data stays readable and devices keep reporting). Importing a valid
license restores full operation without recreating anything.

### Trust model

Licensing is an offline Ed25519 signature flow, and the two keys never share
a location:

| Key | Lives where | May enter the cluster? |
|-----|-------------|------------------------|
| Private signing key (`vendor-ed25519.pem`) | Offline/trusted host, outside Git, mode 0600 | **Never** — not in an image, not in a Secret, not mounted into any Pod |
| Public verification key (`public.pem`) | Derived from the private key; baked into the image or supplied as a Secret | Yes — this is the only key material the server receives |

The server only verifies signatures: `internal/license` has no private-key
configuration at all. A private key mounted into the cluster provides no
functionality, and its exposure would let an attacker mint arbitrary
licenses; treat any private key that ever enters a Secret as compromised.

### Kubernetes end-to-end runbook

A Kubernetes deployment uses its own fresh key pair. Because a new
deployment has no license row yet, establishing a new trust anchor at this
point costs nothing — there are no existing licenses to re-sign. Run steps
1–2 on the offline signing host from the **repository root**; only the public
key and the signed envelope ever leave it.

Commands below use plain `kubectl`; on a k3s node whose default kubeconfig
does not trust the cluster API CA, prefix every call with `sudo -n` (or
`alias kubectl='sudo -n kubectl'`).

#### 0. Pre-flight: confirm the read-only starting state

Select the application Pod by its component label — never by
`deploy/nereidlink`, whose selector also matches the NATS and PostgreSQL
Pods:

```bash
POD=$(kubectl -n nereidlink get pod \
  -l app.kubernetes.io/component=app \
  -o jsonpath='{.items[0].metadata.name}')

kubectl -n nereidlink exec "${POD}" -c nereidlink -- \
  /usr/local/bin/nereidlink -c /etc/nereidlink/config.d license status
```

A fresh deployment returns:

```json
{"installed":false,"tenant_cap":0,"tenant_count":0,
 "issued_at":"0001-01-01T00:00:00Z","expires_at":"0001-01-01T00:00:00Z",
 "features":[],"read_only":true,"reason":"deployment license is not installed"}
```

This is the state the rest of the runbook removes. Keep this command handy;
the same call is the final verification in step 6.

#### 1. Generate a fresh Ed25519 key pair

```bash
STATE_DIR=../.deployment/deploy-values/nereidlink/license
PRIVATE_KEY="${STATE_DIR}/private/vendor-ed25519.pem"
PUBLIC_KEY="${STATE_DIR}/public.pem"

install -d -m 700 "$(dirname "${PRIVATE_KEY}")"
test ! -e "${PRIVATE_KEY}"          # never overwrite an existing signing key
openssl genpkey -algorithm Ed25519 -out "${PRIVATE_KEY}"
chmod 600 "${PRIVATE_KEY}"
openssl pkey -in "${PRIVATE_KEY}" -pubout -out "${PUBLIC_KEY}"
```

Back up the private key through your normal sealed-vault process. Losing it
means no future license can be signed for this trust anchor.

#### 2. Sign the deployment license with the private key

```bash
go run ./tools/license-sign \
  --key "${PRIVATE_KEY}" \
  --scope '*' --customer 'Your Customer' --years 10 \
  --out "${STATE_DIR}/deployment.license.json"
chmod 600 "${STATE_DIR}/deployment.license.json"
```

The envelope is JSON (`base64` payload plus a detached Ed25519 signature).
See `tools/license-sign/README.md` for tenant-scope licenses and the full
flag reference. The private key is used only here, on the signing host.

#### 3. Bind the public key through a Secret and Helm

Only the **public** key goes into the cluster. Validate the PEM header first,
create the Secret (the chart's default Secret key is `public.pem`), then
roll the chart:

```bash
head -1 "${PUBLIC_KEY}"    # must show: -----BEGIN PUBLIC KEY-----
openssl pkey -in "${PUBLIC_KEY}" -pubin -noout   # must parse as a public key

kubectl -n nereidlink create secret generic nereidlink-license-key \
  --from-file=public.pem="${PUBLIC_KEY}"

# --reuse-values keeps every other release setting when values are passed ad
# hoc; values-file based releases can pass license.existingSecret there instead.
helm upgrade nereidlink deploy/kubernetes/helm/nereidlink -n nereidlink \
  --reuse-values \
  --set license.existingSecret=nereidlink-license-key
```

The chart mounts this file read-only at
`/etc/nereidlink/license/public.pem`, shadowing the baked key. A Secret that
accidentally contains `BEGIN PRIVATE KEY` material makes the server's
public-key parse fail at startup and sends the Pod into CrashLoopBackOff.

#### 4. Verify the binding before importing anything

```bash
# Rollout completes and the new Pod is stable (no CrashLoopBackOff).
kubectl -n nereidlink rollout status deployment/nereidlink

# The mount is wired to the Secret, read-only.
kubectl -n nereidlink get deployment nereidlink -o go-template \
  '{{range (index .spec.template.spec.containers 0).volumeMounts}}{{if eq .name "license-key"}}{{.mountPath}} ro={{.readOnly}}{{"\n"}}{{end}}{{end}}'

# Fingerprints of the local public key and the Secret content must match.
openssl pkey -in "${PUBLIC_KEY}" -pubin -pubout -outform DER | sha256sum
kubectl -n nereidlink get secret nereidlink-license-key \
  -o go-template='{{index .data "public.pem"}}' | base64 -d \
  | openssl pkey -pubin -pubout -outform DER | sha256sum
```

A successful `license status` call (step 6) is the final proof that the
mounted key parsed; on k3s prefix `kubectl` with `sudo -n` as usual.

#### 5. Import the license envelope

Prefer the console: log in as the platform super administrator, open
**System → License → Import license**, and upload
`deployment.license.json` (paste the JSON or choose the file). The envelope
travels over the authenticated API — no node access, `exec` or file copy is
needed. The upload endpoint accepts deployment-scope licenses only.

Alternative supported paths:

- **Idempotent automation:** `deploy/kubernetes/scripts/deploy.sh
  post-deploy` waits for the rollout, checks status, and signs/installs a
  default license only when none exists, preserving any license already
  installed.
- **Manual CLI:** the backend image is distroless — no shell, no `tar` — so
  `kubectl exec -- sh`, `kubectl cp` and in-Pod shell commands do not exist.
  Run a host-built `nereidlink` binary against a temporary PostgreSQL
  port-forward with the same merged ConfigMap/Secret configuration as the
  server, exactly as `post-deploy.sh` does (it builds `config.d`, rewrites
  the DSN to `127.0.0.1:<port>` and points `clock_file` at a host-writable
  path). Tenant-scope licenses can only be installed via this path.

#### 6. Confirm success in the UI

**System → License** must show **Installed** with the customer name, issue
and expiry dates, and no read-only banner; tenant/device creation works
again. The equivalent CLI check must return `"installed":true` with
`"read_only":false`:

```bash
# Select the app Pod by its component label. A bare "kubectl exec
# deploy/nereidlink" also matches the NATS and PostgreSQL Pods (they share
# the name+instance labels) and fails with
# "stat /usr/local/bin/nereidlink: no such file or directory" on those.
POD=$(kubectl -n nereidlink get pod \
  -l app.kubernetes.io/component=app \
  -o jsonpath='{.items[0].metadata.name}')

kubectl -n nereidlink exec "${POD}" -c nereidlink -- \
  /usr/local/bin/nereidlink -c /etc/nereidlink/config.d license status
```

Example healthy output:

```json
{"installed":true,"customer_name":"Your Customer","tenant_count":1,
 "issued_at":"...","expires_at":"...","features":[],"read_only":false}
```

`read_only:true` plus a `reason` field explains a degraded state (for
example `"deployment license is not installed"`). Automation must treat
`installed:true` as "leave the existing license untouched".

### License scope

A deployment-scope license (`scope: "*"`) gates tenant count; a tenant-scope
license gates that tenant's device count. Expiry, over-cap and clock-rollback
conditions all degrade the affected scope to read-only rather than stopping
the process. If the public key is rotated after licenses already exist, every
affected license must be re-signed by the matching private key.

## Single host

`deploy/docker-compose/` provides the equivalent split stack
(Postgres-TimescaleDB + NATS-JetStream + the NereidLink backend and frontend
containers) for one machine. Blob/S3 features are disabled in this stack and
firmware OTA works through external URLs only. See
`deploy/docker-compose/README.md`.
