# chrome-sweep: per-version resource sets

## Problem

`cmd/chrome-sweep` currently tests Chrome image tags by re-pointing a single
shared `chrome` Deployment at each tag, one after another. Versions cannot run
concurrently, the previous version is destroyed on each step, and the tool
depends on a pre-existing `chrome` Deployment/Service.

## Goal

Give each Chrome version its own set of non-PVC resources, named by version, so
versions are isolated and can run in bounded parallel. One shared PVC still
collects all frames.

## Resource model

For each version `<ver>`, all in namespace `ci`:

| Resource | Name | Key fields |
|----------|------|-----------|
| Deployment | `chrome-<ver>` | selector + pod labels `app: chrome-<ver>`; container `chrome` image `<repo>:<ver>` |
| Service | `chrome-<ver>` | selector `app: chrome-<ver>`, port 9222 |
| NetworkPolicy | `chrome-<ver>-lockdown` | podSelector `app: chrome-<ver>`; ingress from `role: chrome-controller` on 9222; egress DNS + to `app: anubis` |
| Job | `chrome-smoke-<ver>` | `chrome-bully` arg `-cdp-url=http://chrome-<ver>:9222`; `smoke` container curls `chrome-<ver>:9222`; pod label `role: chrome-controller`; mounts shared PVC |

Shared, **not** templated per version (prerequisites / singletons):

- `PVC/chrome-bully-data` (RWX) — one, holds every version's frames.
- Anubis deployment + `anubis-key` secret.
- One `chrome-sweep-collector` busybox pod (mounts the PVC read-only for
  `kubectl cp`).

The per-version NetworkPolicy is **required**, not cosmetic: NetworkPolicies are
additive-deny and only restrict pods they select. A `chrome-<ver>` pod that no
policy selects would have its unauthenticated CDP port (full browser control)
open to the whole namespace and unrestricted egress. The per-version policy
restores the lockdown the current single `chrome-lockdown` policy provides.

Versions are isolated from non-controller pods, but not from each other: ingress
stays keyed on the shared `role: chrome-controller` label (matches current
intent — the policy exists to keep non-controllers out, not to partition
controllers).

## Templating approach

The tool reads the base manifests as templates and rewrites them per version
with pure, unit-testable mutation functions. Base manifests keep their current
single-version form (name `chrome`, label `app: chrome`, service host `chrome`):

- `k8s/deployment.yaml`, `k8s/service.yaml`, `k8s/networkpolicy.yaml`,
  `k8s/smoke-job.yaml`.

Mutation functions (each takes a decoded typed object + the version + derived
values, returns the mutated object):

- `retargetDeployment(dep, versionedName, image)` — set name, selector
  `matchLabels[app]`, pod template `labels[app]` to `versionedName`; set the
  `chrome` container image.
- `retargetService(svc, versionedName)` — set name and selector `app`.
- `retargetNetworkPolicy(np, versionedName)` — set name to
  `<versionedName>-lockdown` and podSelector `app` to `versionedName`.
- `retargetJob(job, baseHost, versionedHost)` — set name to
  `chrome-smoke-<ver>`; rewrite the `chrome-bully` container's `-cdp-url=` arg
  to `http://<versionedHost>:9222`; string-replace `<baseHost>:9222` →
  `<versionedHost>:9222` in the `smoke` container's args.

`baseHost` is the base Deployment/Service name (`chrome`); `versionedHost` is
`chrome-<ver>`. `versionedName` for a version is `chrome-<ver>` where `<ver>`
is the tag.

## Tool changes

New / changed `Cluster` methods (client-go, namespace-scoped, fake-clientset
testable):

- `CreateOrReplace{Deployment,Service,NetworkPolicy}(ctx, obj)` — create;
  on AlreadyExists, delete + wait-clear + create (mirrors existing `ReplaceJob`
  idempotency, for safe re-runs).
- `DeleteVersionResources(ctx, versionedName)` — delete the version's
  Deployment, Service, NetworkPolicy (`<name>-lockdown`), and Job
  (`chrome-smoke-<ver>`); tolerate NotFound; best-effort (log warn on failure).
- Reuse existing `WaitDeploymentReady`, `ReplaceJob`, `WaitJob`,
  `JobContainerLogs`, `EnsureCollector`, `DeleteCollector`, `CopyFrame`.
- Add manifest loaders `loadDeployment`, `loadService`, `loadNetworkPolicy`
  analogous to `loadJob`.
- Remove `SetImage` and `TestSetImage` — the create path sets the image at
  creation time, so the in-place image patch is dead.

Removed prerequisite: the `chrome` Deployment/Service no longer need to
pre-exist; the tool creates them. Prerequisites become: the PVC, Anubis, and the
`anubis-key` secret.

## Run model

Bounded-parallel worker pool over the versions:

- New flag `-parallelism` (default **8**): max concurrent versions in flight.
- New flags for base manifests: `-deployment-manifest` (`k8s/deployment.yaml`),
  `-service-manifest` (`k8s/service.yaml`), `-networkpolicy-manifest`
  (`k8s/networkpolicy.yaml`); keep `-job-manifest`.
- `-deployment`/`-container` become the base name/container for templating
  (default `chrome`/`chrome`).

Per version (one worker slot):

1. retarget + `CreateOrReplace` Deployment, Service, NetworkPolicy.
2. `WaitDeploymentReady(chrome-<ver>)`.
3. retarget + `ReplaceJob(chrome-smoke-<ver>)`.
4. `WaitJob(chrome-smoke-<ver>)`.
5. collect `reportedUA` (smoke logs) + `capturedFramePath` (chrome-bully logs)
   + `CopyFrame` to `./var/.../frames/<ver>.png`.
6. `DeleteVersionResources(chrome-<ver>)` — tear down (frames already copied
   off; the shared collector + PVC persist).
7. record one `Result`.

A failure on one version is recorded (Status mapping unchanged: create/job
errors → `error`, rollout timeout → `not-ready`, job Failed → `fail`, else
`pass`) and does not stop the others. `EnsureCollector` runs once before the
pool; `DeleteCollector` runs once after. Report format (`report.md` /
`report.json` / printed markdown / non-zero exit on any non-pass) is unchanged.

Results are written to a pre-sized slice indexed by version position (each
worker owns its index) so the report order matches the argument order
regardless of completion order.

## Testing

- Pure mutation functions: table tests asserting each retarget sets exactly the
  right name/label/selector/image/cdp-url and leaves the rest intact
  (`retargetJob` asserts both the `-cdp-url` arg and the `smoke` script host are
  rewritten and the `Host: localhost:9222` header is left alone).
- Fake-clientset: `CreateOrReplace*` create-when-absent and replace-when-present
  paths; `DeleteVersionResources` removes the set and tolerates NotFound.
- The bounded-parallel orchestration and real frame copy are covered by the
  documented real-cluster run (needs a live cluster), as in the original plan.

## Out of scope

- Per-version isolation *between* controller pods.
- Changing the base manifests' single-version form (they stay as templates).
- Leaving resources up for inspection / a `-keep` flag (teardown is
  unconditional; frames are already copied to `./var`).
