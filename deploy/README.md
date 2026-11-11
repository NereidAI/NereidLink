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
(`/ws`), `/api`, `/metrics`, `/healthz` and `/auth` (the NS SSO callback)
go to the backend Service, while `/` goes to the stateless `frontend`
Deployment/Service (nginx, default 2 replicas, no PVC, no arch pin). The
frontend image never proxies to the backend; every new non-`/v1` backend
HTTP route must be added to the Ingress path table in the same change.

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
(tenant and device creation are blocked). The standard `deploy.sh
install|upgrade` path checks `nereidlink license status`, signs a default
`scope="*"` license with the offline vendor key when none is installed, and
imports it with `nereidlink license install`. It preserves any existing
license on rerun. The application image is distroless, so the post-deploy
script builds the current host CLI and uses a temporary PostgreSQL
port-forward; the private key never enters the cluster or image.

For a manual/raw-Helm installation:

1. Sign a deployment license offline (the private key stays outside Git and must
   never be shipped):

   ```bash
   go run ./tools/license-sign \
     --key ../.deployment/deploy-values/nereidlink/license/private/vendor-ed25519.pem \
     --scope '*' --customer 'Your Customer' --years 10 \
     --out deployment.license.json
   ```

2. Import it, either in the web console as the platform super administrator
   (**System → License → Import license**; paste or choose the JSON file) or
   with the supported CLI using the same merged configuration as the server:

   ```bash
   nereidlink -c /etc/nereidlink/config.d license status
   nereidlink -c /etc/nereidlink/config.d license install \
     --file deployment.license.json
   ```

   `license status` emits JSON, including `installed`, expiry and gate state;
   automation must not install a default license when `installed` is true.

The server verifies the envelope with an Ed25519 **public** key. That key is
baked at `/etc/nereidlink/license/public.pem` for zero-config use, but it can
be rotated without rebuilding the image by supplying a Secret and setting
`license.existingSecret`:

```bash
kubectl -n nereidlink create secret generic nereidlink-license-key \
  --from-file=public.pem=configs/license/public.pem
helm upgrade nereidlink deploy/kubernetes/helm/nereidlink -n nereidlink \
  --set license.existingSecret=nereidlink-license-key
```

A deployment-scope license (`scope: "*"`) gates tenant count; a tenant-scope
license gates that tenant's device count. The administrator upload endpoint
accepts deployment scope only; install a tenant license via the CLI.

## Single host

`deploy/docker-compose/` provides the equivalent split stack
(Postgres-TimescaleDB + NATS-JetStream + the NereidLink backend and frontend
containers) for one machine. Blob/S3 features are disabled in this stack and
firmware OTA works through external URLs only. See
`deploy/docker-compose/README.md`.
