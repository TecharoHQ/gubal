# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

Gubal proves/smoke-tests **archived browser versions** (Chrome and Firefox eras) on Kubernetes by driving them through an [Anubis](https://github.com/TecharoHQ/anubis) challenge and asserting they behave like a real, non-headless browser. The browsers are untrusted (many carry unpatched sandbox-escape CVEs), so the isolation boundary is the pod/VM edge (Kata Containers) plus tight NetworkPolicies, not Chrome's own sandbox.

Go module `github.com/TecharoHQ/gubal`, Go 1.26.4.

## Common commands

```bash
# Build a binary — ALWAYS into ./var, never the repo root:
go build -o ./var/chrome-sweep ./cmd/chrome-sweep

# Test everything / a single test / with the race detector:
go test ./...
go test ./chromesweep/ -run TestRetargetJob -v
go test -race ./chromesweep/
go vet ./...

# Regenerate protobuf/Twirp code after editing pb/*.proto (outputs to gen/):
buf lint
buf generate

# Build a service image (build context is the repo root):
docker build -f docker/Dockerfile.gubald -t ghcr.io/techarohq/gubal/gubald:latest .

# Mass-build the archived-browser "era" images and push to GHCR:
bash scripts/build-chrome-images.sh --only 120,150 --push   # env: REGISTRY, CHROME_BUCKET_URL
bash scripts/build-firefox-images.sh --push
```

CI (`.github/workflows/ci.yml`) runs `go test ./...` and builds every service image on each push/PR, publishing to GHCR only on push to `main`. The era-image mass builds are separate `workflow_dispatch` workflows.

## Architecture

Four binaries under `cmd/`, one importable library, plus proto and k8s manifests:

- **`chromesweep/`** (module-root package, the core library) — sweeps a list of Chrome image tags. For each tag it *creates* a per-version resource set named `chrome-<tag>` (Deployment, Service, `chrome-<tag>-lockdown` NetworkPolicy) plus a `chrome-smoke-<tag>` Job, waits for rollout, runs the Job, reads pod logs, `kubectl cp`s the captured frame off a shared busybox collector pod, then tears the version's resources down. Runs versions in a bounded-parallel worker pool. `Run(ctx, cluster, cfg, framesDir) → Report`. The pure pieces (`retarget*`, log/version parsing, report rendering, bundling) are unit-tested; client-go ops are tested with the fake clientset; the parallel orchestration is only exercised by a real cluster.
- **`cmd/chrome-sweep`** — a thin CLI over `chromesweep` (flags → `Config`, build kubeconfig client, run, write `report.zip`/`report.md`).
- **`cmd/gubald`** — the control-plane service. A Twirp `SmokeTestService` (behind SigV4A auth verified against `iamd`) that maps a request to a `chromesweep.Config`, runs a sweep, and returns `{success, markdown report, per-version results}`. Serves the API + `/healthz` on `:9080` and Prometheus `/metrics` on `:9081`.
- **`cmd/gubalctl`** — client CLI that SigV4A-signs and submits a smoke-test build to a gubald URL.
- **`cmd/chrome-bully`** — the CDP controller that runs *inside* the smoke Job: drives a Chrome pod through Anubis, waits for expected text, screenshots to the PVC.

Data flow for a sweep: `gubald`/`chrome-sweep` → `chromesweep.Run` → per-version k8s resources → `chrome-smoke-<tag>` Job (whose `chrome-bully` container drives the `chrome-<tag>` pod through Anubis) → frames on the `chrome-bully-data` PVC → collected via `kubectl cp` → `Report`.

`pb/*.proto` → `gen/` (via `buf`, with `protovalidate` field rules). `k8s/` holds the base manifests (deployment, service, networkpolicy, smoke-job, pvc, `anubis/`). `manifest/app.yaml` deploys gubald via the cluster's `x.within.website/v1` App CRD, which also generates its RBAC from `spec.role`.

## Non-obvious cross-cutting conventions

- **Binaries build into `./var`**, never the repo root (a stray `./gubald` etc. is a build mistake).
- **`chromesweep` reads the `k8s/*.yaml` manifests from disk at runtime**, relative to the working directory (`DefaultConfig` paths like `k8s/deployment.yaml`). Any image running it (e.g. `gubald`) must ship those files under its `WORKDIR` **and** include a `kubectl` binary — `chromesweep.CopyFrame` shells out to `kubectl cp`.
- **The per-version NetworkPolicy is load-bearing security, not cosmetic.** CDP on port 9222 is unauthenticated full control of the browser; a `chrome-<tag>` pod with no policy selecting it would be wide open. Always create the NetworkPolicy alongside the Deployment.
- **CDP quirks the manifests/probes work around:** DevTools binds `127.0.0.1` only (an entrypoint socat bridge re-exposes 9222) and rejects DNS-name `Host` headers — reach Chrome by pod IP or send `Host: localhost:9222`. See `k8s/README.md`.
- **All CLIs use flagenv:** `flagenv.Parse()` then `flag.Parse()`. Kebab-case flags map to `UPPER_SNAKE_CASE` env vars (`-access-key-id` → `ACCESS_KEY_ID`). Use `log/slog` (JSON to stderr), never `log`.
- **protovalidate rules compile at first validation (runtime), not build time** — a malformed rule surfaces as a Twirp `invalid_argument: compilation error`, so validation is covered by a unit test (`cmd/gubald/svc/smoketest`). On a `repeated` field, per-element constraints go under `repeated.items` (not directly on the field).
- **Dependency pin:** the `buf.build/gen/go/bufbuild/protovalidate/...` module version must match the one `within.website/x` uses, so `buf.build/go/protovalidate` compiles. If `go mod tidy` shifts it and the build breaks, re-align it rather than bumping protovalidate.
