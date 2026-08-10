# Demo fixture: a server that looks like somebody's actual server.
#
# Not a test fixture. This exists so the README's screen recordings can be made
# against something real — a live sshd, real systemd units, a real Docker
# daemon, real listening ports — without pointing a camera at anybody's
# infrastructure. Every address on screen is 127.0.0.1 and every credential is
# in this file, so there is nothing to blur out afterwards.
#
# The alternative is recording a real server and hoping the edit catches every
# hostname, IP and project name. This is safer and it is reproducible: anyone
# can regenerate the media.
#
#   docker build -t litedeck-demo testdata/demo
#   docker run -d --name litedeck-demo --privileged --cgroupns=host \
#     -v /sys/fs/cgroup:/sys/fs/cgroup:rw -p 2299:22 litedeck-demo
#
# Credentials are hard-coded because this image is disposable and bound to a
# loopback port. Never publish it.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
ENV container=docker

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      systemd systemd-sysv dbus openssh-server sudo iproute2 \
      nginx docker.io ca-certificates curl vim less \
 && rm -rf /var/lib/apt/lists/*

# Units that assume real hardware have nothing to manage in a container.
RUN find /lib/systemd/system/sysinit.target.wants/ -name 'systemd-tmpfiles-setup*' -delete \
 && rm -f /lib/systemd/system/multi-user.target.wants/getty.target \
          /lib/systemd/system/local-fs.target.wants/* \
          /lib/systemd/system/sockets.target.wants/*udev* \
          /lib/systemd/system/basic.target.wants/*

RUN mkdir -p /run/sshd \
 && ssh-keygen -A \
 && useradd -m -s /bin/bash deploy \
 && echo 'deploy:demo-fixture-pw' | chpasswd \
 && adduser deploy sudo \
 && adduser deploy docker \
 && systemctl enable ssh nginx docker

# ── A project tree that reads like one somebody works in ──────────────────
#
# The file explorer with a single file in it says nothing about whether the
# explorer is any good. This is a small but plausible Node service.
RUN mkdir -p /srv/api/src/routes /srv/api/src/lib /srv/api/test /srv/api/logs \
             /srv/api/.github/workflows /srv/api/node_modules/.bin

RUN set -eux; cd /srv/api; \
    printf '%s\n' \
      '{' \
      '  "name": "orders-api",' \
      '  "version": "2.4.1",' \
      '  "type": "module",' \
      '  "scripts": {' \
      '    "dev": "tsx watch src/server.ts",' \
      '    "build": "tsc -p tsconfig.json",' \
      '    "test": "vitest run"' \
      '  },' \
      '  "dependencies": {' \
      '    "fastify": "^4.26.2",' \
      '    "pg": "^8.11.5",' \
      '    "ioredis": "^5.4.1",' \
      '    "zod": "^3.23.8"' \
      '  }' \
      '}' > package.json; \
    printf '%s\n' \
      'import Fastify from "fastify"' \
      'import { registerRoutes } from "./routes/index.js"' \
      'import { pool } from "./lib/db.js"' \
      '' \
      'const app = Fastify({ logger: true })' \
      'const PORT = Number(process.env.PORT ?? 3000)' \
      '' \
      'registerRoutes(app)' \
      '' \
      'app.addHook("onClose", async () => {' \
      '  await pool.end()' \
      '})' \
      '' \
      'await app.listen({ port: PORT, host: "0.0.0.0" })' > src/server.ts; \
    printf '%s\n' \
      'import type { FastifyInstance } from "fastify"' \
      'import { orders } from "./orders.js"' \
      'import { health } from "./health.js"' \
      '' \
      'export function registerRoutes(app: FastifyInstance) {' \
      '  app.register(orders, { prefix: "/v1/orders" })' \
      '  app.register(health)' \
      '}' > src/routes/index.ts; \
    printf '%s\n' \
      'import { z } from "zod"' \
      '' \
      'const CreateOrder = z.object({' \
      '  customerId: z.string().uuid(),' \
      '  items: z.array(z.object({ sku: z.string(), qty: z.number().int().positive() })),' \
      '})' \
      '' \
      '// TODO: idempotency key — a retried POST currently creates a second order.' \
      'export async function orders(app) {' \
      '  app.post("/", async (req, reply) => {' \
      '    const body = CreateOrder.parse(req.body)' \
      '    return reply.code(201).send({ id: crypto.randomUUID(), ...body })' \
      '  })' \
      '}' > src/routes/orders.ts; \
    printf '%s\n' \
      'export async function health(app) {' \
      '  app.get("/healthz", async () => ({ ok: true, uptime: process.uptime() }))' \
      '}' > src/routes/health.ts; \
    printf '%s\n' \
      'import pg from "pg"' \
      '' \
      'export const pool = new pg.Pool({' \
      '  connectionString: process.env.DATABASE_URL,' \
      '  max: 10,' \
      '  idleTimeoutMillis: 30_000,' \
      '})' > src/lib/db.ts; \
    printf '%s\n' \
      'import Redis from "ioredis"' \
      'export const redis = new Redis(process.env.REDIS_URL ?? "redis://127.0.0.1:6379")' > src/lib/cache.ts; \
    printf '%s\n' \
      'export const log = (level, msg, extra = {}) =>' \
      '  console.log(JSON.stringify({ level, msg, ts: Date.now(), ...extra }))' > src/lib/logger.ts; \
    printf '%s\n' \
      'import { describe, it, expect } from "vitest"' \
      'import { build } from "./helpers.js"' \
      '' \
      'describe("POST /v1/orders", () => {' \
      '  it("rejects an empty basket", async () => {' \
      '    const app = await build()' \
      '    const res = await app.inject({ method: "POST", url: "/v1/orders", payload: { items: [] } })' \
      '    expect(res.statusCode).toBe(400)' \
      '  })' \
      '})' > test/orders.test.ts; \
    printf '%s\n' \
      '{' \
      '  "compilerOptions": {' \
      '    "target": "ES2022",' \
      '    "module": "NodeNext",' \
      '    "strict": true,' \
      '    "outDir": "dist"' \
      '  },' \
      '  "include": ["src/**/*.ts"]' \
      '}' > tsconfig.json; \
    printf '%s\n' \
      'NODE_ENV=production' \
      'PORT=3000' \
      'DATABASE_URL=postgres://orders:CHANGEME@127.0.0.1:5432/orders' \
      'REDIS_URL=redis://127.0.0.1:6379' \
      'LOG_LEVEL=info' > .env.example; \
    printf '%s\n' \
      'node_modules/' \
      'dist/' \
      '.env' \
      '*.log' > .gitignore; \
    printf '%s\n' \
      '# orders-api' \
      '' \
      'Order intake for the storefront. Fastify + Postgres + Redis.' \
      '' \
      '    npm install' \
      '    npm run dev' \
      '' \
      'Deployed by `deploy.yml` on a push to `main`.' > README.md; \
    printf '%s\n' \
      'services:' \
      '  redis:' \
      '    image: redis:7-alpine' \
      '    ports: ["6379:6379"]' \
      '  cache:' \
      '    image: nginx:alpine' \
      '    ports: ["8080:80"]' > docker-compose.yml; \
    printf '%s\n' \
      'name: deploy' \
      'on:' \
      '  push:' \
      '    branches: [main]' \
      'jobs:' \
      '  ship:' \
      '    runs-on: ubuntu-latest' \
      '    steps:' \
      '      - uses: actions/checkout@v4' \
      '      - run: npm ci && npm run build' > .github/workflows/deploy.yml; \
    for i in 1 2 3 4 5 6; do \
      printf '{"level":"info","msg":"request","route":"/v1/orders","ms":%s}\n' "$i" >> logs/app.log; \
    done; \
    printf '%s\n' '2026-08-09 03:14:02 WARN pool exhausted, waiting 250ms' \
                  '2026-08-09 03:14:02 ERRO ECONNREFUSED 127.0.0.1:5432' > logs/error.log; \
    chown -R deploy:deploy /srv/api

# ── Units named the way somebody's units are named ────────────────────────
RUN set -eux; \
    printf '%s\n' '[Unit]' 'Description=Orders API (Fastify)' 'After=network.target' \
      '[Service]' 'ExecStart=/bin/bash -c "while :; do sleep 30; done"' 'Restart=always' \
      '[Install]' 'WantedBy=multi-user.target' > /etc/systemd/system/orders-api.service; \
    printf '%s\n' '[Unit]' 'Description=Order fulfilment worker' \
      '[Service]' 'ExecStart=/bin/bash -c "while :; do sleep 30; done"' 'Restart=always' \
      '[Install]' 'WantedBy=multi-user.target' > /etc/systemd/system/orders-worker.service; \
    printf '%s\n' '[Unit]' 'Description=Prometheus node exporter' \
      '[Service]' 'ExecStart=/bin/bash -c "while :; do sleep 30; done"' 'Restart=always' \
      '[Install]' 'WantedBy=multi-user.target' > /etc/systemd/system/node-exporter.service; \
    printf '%s\n' '[Unit]' 'Description=Nightly database backup' \
      '[Service]' 'Type=oneshot' 'ExecStart=/bin/false' \
      '[Install]' 'WantedBy=multi-user.target' > /etc/systemd/system/db-backup.service; \
    printf '%s\n' '[Unit]' 'Description=Renew Let'"'"'s Encrypt certificates' \
      '[Service]' 'Type=oneshot' 'ExecStart=/bin/false' \
      '[Install]' 'WantedBy=multi-user.target' > /etc/systemd/system/certbot-renew.service; \
    systemctl enable orders-api orders-worker node-exporter

# Docker inside Docker cannot stack overlay on overlay — every `docker run` in
# here dies on "mount source: overlay ... invalid argument" with the default
# driver. vfs is slow and wastes disk, which is the correct trade for a fixture
# that holds six small containers. The containerd snapshotter has to go too: it
# does its own overlay mounts and ignores storage-driver.
RUN mkdir -p /etc/docker \
 && printf '%s\n' '{"storage-driver":"vfs","features":{"containerd-snapshotter":false}}' \
      > /etc/docker/daemon.json

# nginx gets a second listener so the network tab has something to show beyond
# port 80 alone.
RUN printf '%s\n' \
      'server {' \
      '  listen 8443;' \
      '  server_name _;' \
      '  location / { return 200 "ok\n"; }' \
      '}' > /etc/nginx/sites-enabled/admin.conf

EXPOSE 22
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
