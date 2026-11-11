<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# NereidLink split stack (Docker Compose)

Self-contained single-host deployment with two edge modes:

| Component | Image source |
|-----------|--------------|
| NereidLink backend (HTTP/WS 8080, MQTT 1883/8883) | `ghcr.io/nereidai/nereidlink-backend` (public; air-gapped hosts resolve it via the node registry mirror) |
| NereidLink frontend (static nginx console, port 80) | `ghcr.io/nereidai/nereidlink-frontend` (public; same mirror) |
| PostgreSQL 16 + TimescaleDB | `timescale/timescaledb:2.17.2-pg16` (public) |
| NATS 2.10 + JetStream | `nats:2.10-alpine` (public) |
| Nginx + certbot edge (optional) | `jonasal/nginx-certbot:5.6.0-alpine` (public) |

Blob/S3 features are disabled in this stack; firmware OTA works via external
URLs only.

## Modes

- **Mode A — behind an existing host nginx (default, used on the test
  server).** The host nginx terminates TLS and proxies to the app's
  loopback-only `18080`. Certificates are managed by the host's certbot
  (auto-renewal via the certbot systemd timer).
- **Mode B — standalone edge.** Start the bundled nginx-certbot service with
  the `standalone-edge` profile; it listens on host `10080`/`10443` and
  requests Let's Encrypt certificates itself.

### Local trial without a host nginx

The frontend container serves static assets only and the console uses
same-origin relative paths, so opening the loopback `18081` port directly
shows the shell but API calls fail. For a quick local trial without
configuring the host nginx, a plain-HTTP loopback edge on `127.0.0.1:18082`
is provided as an override (not part of production deployment):

```bash
cp docker-compose.local.yml.example docker-compose.local.yml
docker compose -f docker-compose.yml -f docker-compose.local.yml up -d
# open http://127.0.0.1:18082
```

`docker-compose.local.yml` is git-ignored; its tracked source is the
`.example` file and the edge rules live in `nginx/edge-local.conf`.

## Port mapping

| Host port | Container | Mode | Purpose |
|-----------|-----------|------|---------|
| 127.0.0.1:18080 | nereidlink:8080 | A | Backend HTTP/WS (`/v1`, `/ws`, `/api`, `/metrics`, `/healthz`, `/auth`), reached by host nginx only |
| 127.0.0.1:18081 | frontend:80 | A | Static console (SPA), reached by host nginx only |
| 10080 | nginx:80 | B | HTTP, ACME challenge, redirect to HTTPS |
| 10443 | nginx:443 | B | HTTPS + WebSocket (`wss://`), split to backend/frontend |
| 11883 | nereidlink:1883 | both | Plain MQTT |
| 18833 | nereidlink:8883 | both | MQTTS (platform device CA, **not** Let's Encrypt) |

PostgreSQL and NATS are never published to the host.

## Prerequisites

1. A public DNS name pointing at this host. The examples below use the reserved
   **nereidlink.example.com** placeholder; replace it with your actual hostname
   in the configuration and commands.
2. Docker Engine with the Compose plugin.
3. Air-gapped hosts only: the node-level containerd registry mirror must be
   configured (NereidOverview `harbor-sync.sh node-mirror`, treaty §18),
   using credentials supplied outside the repository:

   ```bash
   docker login <harbor-host>
   ```

## Deployment environment file

`.env.example` remains the canonical, non-sensitive template. Keep the actual
environment outside the repository at
`../.deployment/deploy-values/nereidlink/compose.env` (the path below is the
same location as seen from `deploy/docker-compose/`). Create it once without
replacing an existing operator file, and restrict both directory and file:

```bash
cd deploy/docker-compose
export COMPOSE_ENV_FILES=../../../.deployment/deploy-values/nereidlink/compose.env
install -d -m 700 "$(dirname "${COMPOSE_ENV_FILES}")"
if [ ! -f "${COMPOSE_ENV_FILES}" ]; then
  install -m 600 .env.example "${COMPOSE_ENV_FILES}"
fi
chmod 600 "${COMPOSE_ENV_FILES}"
```

Compose reads the file through `COMPOSE_ENV_FILES`; `--env-file
"${COMPOSE_ENV_FILES}"` is the equivalent per-command form. Re-export the
variable from this directory in each new operator shell. For an independent
installation without the central layout, unset `COMPOSE_ENV_FILES`, copy
`.env.example` to local `.env`, and use the same commands.

## Mode A — deploy behind the host nginx

### 1. Start the stack

With `COMPOSE_ENV_FILES` set as above:

```bash
vi "${COMPOSE_ENV_FILES}"     # set NL_VERSION (CERTBOT_EMAIL is unused in Mode A)
docker compose up -d
docker compose ps             # postgres, nats, nereidlink, frontend must be up
```

### 2. Add the host nginx site

Create `/etc/nginx/sites-available/nereidlink.example.com`. The edge
splits backend prefixes to `18080` and everything else (the SPA) to the
frontend container on `18081`:

```nginx
server {
    listen 80;
    listen [::]:80;
    server_name nereidlink.example.com;

    # REST API, WebSocket, Swagger, metrics, health, SSO callback.
    location ~ ^/(v1|ws|api|metrics|healthz|auth)(/|$) {
        proxy_pass http://127.0.0.1:18080;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade           $http_upgrade;
        proxy_set_header Connection        "upgrade";
        proxy_read_timeout 3600s;
    }

    # Static console (nginx history-mode fallback lives in the container).
    location / {
        proxy_pass http://127.0.0.1:18081;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Enable and reload:

```bash
sudo ln -s /etc/nginx/sites-available/nereidlink.example.com \
           /etc/nginx/sites-enabled/nereidlink.example.com
sudo nginx -t && sudo systemctl reload nginx
```

### 3. Issue the certificate

```bash
sudo certbot --nginx -d nereidlink.example.com
```

Certbot validates over public port 80, rewrites the site to add the 443
server and the HTTP->HTTPS redirect, and reloads nginx. Renewal is automatic
via the existing `certbot.timer`:

```bash
systemctl list-timers | grep certbot
sudo certbot renew --dry-run
```

Then open `https://nereidlink.example.com/`.

## Mode B — standalone edge

Requires public `:80` and `:443` reachability (forward them to host
`10080`/`10443`; Let's Encrypt always validates against public port 80):

With `COMPOSE_ENV_FILES` set as above:

```bash
vi "${COMPOSE_ENV_FILES}"     # set NL_VERSION and a real CERTBOT_EMAIL
docker compose --profile standalone-edge up -d
docker compose logs -f nginx   # watch issuance
```

On first start the nginx container generates a placeholder certificate,
requests the Let's Encrypt certificate, and reloads nginx. Open:

```
https://nereidlink.example.com:10443/
```

## First login

A fresh deployment has `admin@nereidai.local` with an empty password. The
console shows a bootstrap screen where the first visitor sets the
administrator password exactly once. There is no shipped default password.

## License

A fresh deployment has no license and starts read-only (tenant/device creation
blocked). Import a signed license either via **System -> License -> Import
license** or:

```bash
go run ./tools/license-sign \
  --key ../.deployment/deploy-values/nereidlink/license/private/vendor-ed25519.pem \
  --scope '*' --customer 'Test Customer' --years 10 \
  --out deployment.license.json

docker compose cp deployment.license.json nereidlink:/tmp/deployment.license.json
docker compose exec nereidlink \
  nereidlink -c /etc/nereidlink/config.yaml license install \
  --file /tmp/deployment.license.json
```

## Device MQTT access

```
mqtt://nereidlink.example.com:11883
mqtts://nereidlink.example.com:18833
```

The MQTTS listener uses the platform's own device CA, not Let's Encrypt.
Devices must trust that CA (download it from the web console). Its leaf
certificate is re-signed on every stack start with the SAN list defined by
`--hosts` in `docker-compose.yml`; edit that list before deploying elsewhere.

## Upgrading / operations

```bash
# Change NL_VERSION in ${COMPOSE_ENV_FILES}, then:
docker compose pull
docker compose up -d

# Mode B includes the edge:
docker compose --profile standalone-edge pull
docker compose --profile standalone-edge up -d

# Re-run migrations manually if an upgrade needs it (migrate up is idempotent):
docker compose run --rm migrate

# Inspect state:
docker compose ps
docker compose logs -f nereidlink
```

Do **not** delete the `nldata` Docker volume: it holds the device PKI, the
tenant-secret KEK and the license clock. Deleting it makes previously sealed
secrets undecryptable. In Mode B the `letsencrypt` volume can be removed to
force a fresh issuance.

## Troubleshooting

- **HTTP-01 challenge 404 (Mode B)**: the LE validation request is not
  reaching the bundled nginx. Verify with a test file:

  ```bash
  docker compose exec nginx sh -c \
    'mkdir -p /var/www/letsencrypt/.well-known/acme-challenge && \
     echo ok > /var/www/letsencrypt/.well-known/acme-challenge/test'
  curl -i http://nereidlink.example.com/.well-known/acme-challenge/test
  ```

  Confirm public port 80 forwards to host 10080. In Mode A the host certbot
  serves challenges itself; do not proxy them to the container.
- **Mode B rate limited**: set `STAGING=1` in `${COMPOSE_ENV_FILES}` to rehearse,
  then remove the staging certificate (`docker volume rm nereidlink-letsencrypt` after
  `docker compose --profile standalone-edge down`) and set `STAGING=0`.
- **`open /etc/nereidlink/license/public.pem: no such file or directory`**:
  the image was built with `build/Dockerfile`, which does not include the
  license public key. The compose file already bind-mounts it from the
  repository (`../../configs/license/public.pem`); just `git pull` and
  `docker compose up -d`. If only this directory was copied to the host,
  place the key at `./license/public.pem` and adjust the mount source. The
  permanent fix is rebuilding with `deploy/images/backend/Dockerfile`.
- **A one-shot init service failed**: `docker compose logs pki-ensure`
  (or `pki-server` / `migrate`), fix the cause, and rerun with
  `docker compose up -d`; Compose recreates only the failed jobs.
