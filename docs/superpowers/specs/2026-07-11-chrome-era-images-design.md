# Chrome era images — design

Build reproducible Docker images for arbitrary Google Chrome versions, each on the
Ubuntu release contemporary with that Chrome, so the deb's declared dependencies
resolve cleanly. Images run headless as a long-lived CDP server and are proven on
Kubernetes under Kata Containers.

## Inputs

`scripts/archive-chrome-versions.sh` archives Chrome debs to a public Tigris bucket
and writes `chrome/manifest.json`. Each entry:

```json
{
  "major": 120,
  "filename": "google-chrome-stable_120.0.6099.109-1_amd64.deb",
  "sha256": "…",
  "size": 123,
  "source": "https://mirror…/…deb",
  "tigris_key": "chrome/120/google-chrome-stable_120.0.6099.109-1_amd64.deb",
  "uploaded_at": "…"
}
```

Bucket (public): `https://chrome-archive.t3.tigrisfiles.io`. The deb for an entry is
`${BUCKET_URL}/${tigris_key}`. Current manifest holds majors 71–143.

## Chrome major → Ubuntu era

Boundaries on round majors, aligned to each version's release era:

| Chrome major | Base image     |
|--------------|----------------|
| ≤ 79         | `ubuntu:18.04` |
| 80–99        | `ubuntu:20.04` |
| 100–119      | `ubuntu:22.04` |
| 120–139      | `ubuntu:24.04` |
| ≥ 140        | `ubuntu:26.04` |

## Images

- Tag: `ghcr.io/techarohq/gubal/chrome:<full-version>` and `:<major>`.
- Isolation: Kubernetes with `runtimeClassName: kata` (VM boundary). Because Kata is
  the trust boundary, and because ancient Chrome's own sandbox has unpatched escape
  CVEs, `--no-sandbox` inside Kata is the defensible default.

## Dockerfiles (`docker/Dockerfile.chrome-ubuntu-XX.04`, 5 files)

Near-identical; differ only in `FROM`. Each:

- `ARG CHROME_DEB_URL` (required), `ARG CHROME_DEB_SHA256` (optional; verified if set).
- `curl` the deb, optional `sha256sum -c`, then `apt-get install -y <deb> tini fonts…`
  in one transaction so the deb's `Depends:` resolve against the contemporary Ubuntu.
- Installs `ca-certificates`, `curl`, `tini`, `socat`, and a modest font set
  (`fonts-liberation fonts-noto-cjk fonts-noto-color-emoji`).
- Adds a non-root `chrome` user. Does **not** chown `/opt/google/chrome`, so
  `chrome-sandbox` stays `root:root 4755`.
- `COPY docker/entrypoint.sh`, `EXPOSE 9222`, `ENTRYPOINT ["/usr/bin/tini","--", …]`.

## Entrypoint (`docker/entrypoint.sh`)

Derives the full version from `google-chrome --version` and builds a **version-matched
User-Agent with no "Headless"**:

```
Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/<ver> Safari/537.36
```

Launch flags:

- `--headless=new` for major ≥ 109, else `--headless`.
- `--user-agent=<version-matched UA>` (override always; also fixes old-headless UA tell).
- `--remote-debugging-address=0.0.0.0 --remote-debugging-port=9222`.
- `--remote-allow-origins=*` (ignored by Chrome versions that predate it).
- `--disable-dev-shm-usage --disable-gpu --disable-blink-features=AutomationControlled`.

Env knobs (all optional):

- `CHROME_SANDBOX=off` (default) adds `--no-sandbox`; `on` omits it and warns if
  `chrome-sandbox` is not SUID.
- `CHROME_DEBUG_PORT` (9222), `CHROME_DEBUG_ADDRESS` (0.0.0.0), `CHROME_START_URL`
  (about:blank), `CHROME_USER_AGENT` (override the computed UA).
- `CHROME_SOCAT_BRIDGE=true` (**default**) — Chrome listens on `127.0.0.1:<internal>`
  and socat forwards `0.0.0.0:9222` to it. See findings below.

Extra flags after `--` (or `CMD`) are passed through to Chrome.

## Verified Chrome quirks (v120, expected to hold across versions)

1. Chrome **ignores `--remote-debugging-address`** and binds DevTools to `127.0.0.1`
   only. Hence the socat bridge is **on by default** — without it the CDP port is
   unreachable from other pods.
2. DevTools **rejects DNS-name `Host` headers** (anti DNS-rebinding) with HTTP 500;
   IP-literal or `localhost` Host headers return 200. So probes, the smoke Job, and
   controllers must use a pod IP or send `Host: localhost:9222`, not the Service DNS
   name.

## Build script (`scripts/build-chrome-images.sh`)

Ingests the manifest and builds one image per entry.

- Manifest: pulled from `${BUCKET_URL}/chrome/manifest.json`, or `--manifest <path>`.
- Per entry: read `major`, map to Dockerfile, build deb URL from `tigris_key`, run
  `docker build -f <dockerfile> --build-arg CHROME_DEB_URL=… --build-arg
  CHROME_DEB_SHA256=… -t <full> -t <major> .`.
- Flags: `--push`, `--only <major[,major…]>`, `--registry <ref>`, `--dry-run`.
- Continues past per-image failures; prints a pass/fail summary and exits non-zero if
  any build failed.

## Kubernetes proving (`k8s/`)

- `deployment.yaml` — Deployment (`runtimeClassName: kata`) + Service exposing 9222,
  `automountServiceAccountToken: false`, `capabilities: drop [ALL]`,
  `/dev/shm` as `emptyDir{medium: Memory}`, resource limits, readiness probe on
  `/json/version`.
- `networkpolicy.yaml` — default-deny plus an allow only from the controller.
- `smoke-job.yaml` — a curl Job that hits `…:9222/json/version` and asserts the
  reported `User-Agent` does **not** contain `Headless` (proves install + reachability
  + the UA requirement in one shot).
- `README.md` — Kata/gVisor notes and usage.

## Verification

Build at least one image end-to-end from the live Tigris bucket and confirm
`docker run … ` serves `/json/version` with a non-Headless UA.
