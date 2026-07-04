# tagalong

Self-hosted continuous deployment for a single-node **k3s** cluster.

tagalong receives webhooks from **Docker Hub** and **GitHub** (GHCR package publishes) — and can also **poll** registries — and when a new container image is available it updates the matching k3s `Deployment`/`StatefulSet`, watches the rollout, records history, and optionally **purges Cloudflare cache** for the app's URLs. A small web UI (no auth) manages app configs and shows deploy history/activity.

It replaces the manual loop of *edit the image tag in YAML → `kubectl apply`*.

## How it works

```
Docker Hub / GitHub ──webhook──▶  Cloudflare Tunnel ─▶ nginx-proxy-manager ─▶ tagalong (Service)
                                                                                 │
registry (poll) ◀──────────────────────────────────────────────────────────────┤
                                                                                 ▼
                                                              patch image / rollout-restart
                                                              watch rollout ─▶ record history
                                                              ─▶ (optional) Cloudflare purge
```

tagalong runs **in the cluster** as a Deployment with a ServiceAccount and a ClusterRole that can `patch` Deployments/StatefulSets across namespaces. State (apps, history, settings) lives in SQLite on a `local-path` PVC.

## Concepts

An **App** ties an image repo to one or more cluster workloads:

- **image_repo** — normalized `registry/path` (e.g. `docker.io/timdoddcool/robo-dash`, `ghcr.io/timothydodd/cadence/api`). This is the key webhooks match against. You can type `timdoddcool/robo-dash` and it is normalized on save.
- **tag_strategy** — how a pushed/observed tag is judged:
  | Strategy | When it deploys | Action |
  |----------|-----------------|--------|
  | `exact` / `regex` | tag matches a pattern and differs from what's live | patch image to `repo:tag` |
  | `latest` | the tracked rolling tag (default `latest`) is re-pushed or its digest changes | rollout-restart (needs `imagePullPolicy: Always`) |
  | `semver` | a newer semver tag appears (leading `v` and bare `0.6` tolerated) | patch image |

  Pattern presets for `exact`: full git SHA `^[0-9a-f]{40}$`, short SHA `^[0-9a-f]{7,12}$`, metadata-action `^sha-[0-9a-f]+$`.
- **targets** — the workloads to update: `{namespace, kind, name, container}`. Multiple targets let one app drive, e.g., a web + api pair.
- **webhook_token** — per-app secret embedded in the Docker Hub webhook URL.
- **poll** — optional per-app registry polling (fallback for registries that can't send webhooks, e.g. `reg.dodd.rocks`).
- **cf_purge** — optional Cloudflare cache purge after a successful rollout.

## Local development

Requires Go 1.25+ and Node 22+.

```bash
make build          # build the UI then the Go binary (UI embedded via go:embed)
make test           # go test ./...
```

There are two dev loops depending on whether you need a real cluster.

### 1. UI / API iteration — no cluster needed

The backend boots in **degraded mode** without a reachable cluster: everything
works except actual k8s operations, which return an immediate
`kubernetes client not configured` error (no hanging on timeouts). This is ideal
for building the UI and API.

```bash
# terminal 1 — backend on :8080, no cluster, DB at ./dev.db
make dev

# terminal 2 — Vite dev server on :5173 with hot reload, proxies /api + /hooks to :8080
make dev-ui
```

Open <http://localhost:5173>. You can create apps, wire webhooks, browse history,
and see live SSE updates. The registry tag picker and webhook parsing work fully
(they don't need the cluster). Only pressing *Deploy* against a target requires k8s.

Skip `make dev-ui` and open <http://localhost:8080> directly to use the embedded
production UI (requires `make build` first).

### 2. Real deploys — needs a reachable cluster

`make dev` degrades gracefully; to exercise actual rollouts, point tagalong at a
kubeconfig for a cluster it can reach:

```bash
make run            # uses $KUBECONFIG or ~/.kube/config
# or explicitly:
TAGALONG_KUBECONFIG=/path/to/kubeconfig TAGALONG_DB_PATH=./dev.db go run ./cmd/tagalong
```

**Reaching a cluster:**
- **Against your real k3s** — run from the host that can route to the API server
  (e.g. Windows, not WSL, if the node IP is only reachable there).
- **Throwaway local cluster** — spin up [k3d](https://k3d.io) (k3s in Docker) for
  safe testing: `k3d cluster create tagalong-dev`, then `kubectl create deploy nginx
  --image=nginx` and register an app targeting it. `make run` picks up the k3d
  kubeconfig automatically.

Config is all environment variables: `TAGALONG_DB_PATH` (default `/data/tagalong.db`),
`TAGALONG_LISTEN` (default `:8080`), `TAGALONG_KUBECONFIG` (unset = in-cluster, then
degraded).

The JSON API under `/api` is fully usable without the UI (see below).

## Build & publish (CI)

`.github/workflows/docker-publish.yml` builds a multi-arch (amd64/arm64) image
and pushes it to Docker Hub. Add two repo secrets — `DOCKERHUB_USERNAME` and
`DOCKERHUB_TOKEN` (a Docker Hub access token) — then:

- push to `main` → publishes `timdoddcool/tagalong:latest` (+ `:main-<sha>`)
- push a `vX.Y.Z` tag → publishes `:X.Y.Z`, `:X.Y`, and `:latest`
- pull requests build only (no push), to catch breakage early

## Import / export apps as YAML

App configs can be moved in and out as a single declarative YAML file — handy for
backups, cloning a setup between clusters, or reviewing config in git.

- **Export all** — *Apps* page → **Export YAML** downloads `tagalong-apps.yaml`
  (a top-level `apps:` list). Per-app export lives on the app's detail page.
- **Import** — *Apps* page → **Import YAML**, paste a file, **Apply**. Apps are
  matched by `name`: existing ones are **updated**, new ones are **created**
  (nothing is deleted). The whole file is validated before anything is written.
- **Update one app** — an app's detail page has an **Edit as YAML** panel that
  round-trips that single app.

The same endpoints back the UI, so this works headless too:

```bash
curl -s http://localhost:8080/api/apps/export -o tagalong-apps.yaml   # export
curl -s -X POST --data-binary @tagalong-apps.yaml \
  -H 'Content-Type: application/x-yaml' http://localhost:8080/api/apps/import   # import
```

The exported YAML includes each app's `webhook_token`, so re-importing keeps the
webhook URLs (`/hooks/dockerhub/<token>`) stable.

## Deploy to the cluster

1. Build and push the image (from a machine with Docker, or let CI do it):
   ```bash
   make docker IMAGE=timdoddcool/tagalong:latest
   docker push timdoddcool/tagalong:latest
   ```
2. Apply the manifests:
   ```bash
   kubectl apply -f manifests/namespace.yaml
   kubectl apply -f manifests/rbac.yaml
   kubectl apply -f manifests/pvc.yaml
   kubectl apply -f manifests/deployment.yaml
   kubectl apply -f manifests/service.yaml
   ```
   tagalong is now reachable in-cluster at `http://tagalong.tagalong.svc.cluster.local` (port 80).

### Expose the UI and webhooks

Route a public hostname to the `tagalong` Service through the existing tunnel + proxy:

1. **nginx-proxy-manager** → add a Proxy Host `tagalong.dodd.rocks` → `tagalong.tagalong.svc.cluster.local:80`.
2. **Cloudflare Tunnel** (Zero Trust dashboard) → add a public hostname `tagalong.dodd.rocks` → `http://nginx-lb:80` (same pattern the other services use).

Then configure the sources:

- **Docker Hub** (per repo): Repository → Webhooks → add `https://tagalong.dodd.rocks/hooks/dockerhub/<webhook_token>` (copy the token from the app in the UI or the API).
- **GitHub** (org or repo): Settings → Webhooks → Payload URL `https://tagalong.dodd.rocks/hooks/github`, content type `application/json`, secret = the value you set in **Settings → GitHub webhook secret**, events: **Packages** (and/or Registry packages). One org-level webhook covers every GHCR repo; tagalong ignores packages that don't match a configured app.

## API quick reference

```
GET    /api/healthz
GET    /api/apps
POST   /api/apps                      # {name, image_repo, tag_strategy, strategy_conf, targets, ...}
GET    /api/apps/{id}
PUT    /api/apps/{id}
DELETE /api/apps/{id}
POST   /api/apps/{id}/deploy          # {"tag":"..."} to deploy a tag; empty body = rollout-restart
POST   /api/apps/{id}/token/rotate
GET    /api/apps/{id}/status          # live k8s state per target
GET    /api/apps/{id}/tags            # registry tag list (polling must be usable)
GET    /api/events[?app_id=&before_id=&limit=]
GET    /api/events/stream             # Server-Sent Events (live activity)
GET/PUT /api/settings                 # Cloudflare token, GitHub webhook secret (masked on read)
GET/PUT/DELETE /api/settings/registries

POST   /hooks/dockerhub/{token}
POST   /hooks/github
```

Example — register an app and deploy a tag:

```bash
curl -X POST localhost:8080/api/apps -d '{
  "name":"robo-dash","image_repo":"timdoddcool/robo-dash","tag_strategy":"exact",
  "strategy_conf":{"pattern":"^[0-9a-f]{40}$"},"enabled":true,
  "targets":[{"namespace":"default","kind":"Deployment","name":"homedash","container":"robo-dash"}]
}'
curl -X POST localhost:8080/api/apps/1/deploy -d '{"tag":"4fc1300ae6f6b4ede2f1db308e24db1647c4c7f9"}'
```

## Notes & caveats

- **YAML drift.** tagalong patches *live* objects, so your manifest repo (`F:\kuber\project-a`) will drift from what's running. Each deploy event records `old_image → new_image`; use that to sync the YAML when convenient. Keeping the repo in sync is out of scope for tagalong.
- **Secrets** (Cloudflare token, GitHub webhook secret, registry passwords) are stored in the SQLite DB in plaintext. The DB is on a cluster PVC; treat it accordingly. There is no UI auth yet — keep the public hostname behind the tunnel/proxy and don't expose `/api` beyond your LAN.
- **`latest`/rolling tags** only redeploy correctly if the workload uses `imagePullPolicy: Always`.
- **Cloudflare purge** uses `purge_everything` or a `files` list (both work on the Free plan; purge-by-hostname/prefix is Enterprise-only). URLs are chunked at 30 per API call.
