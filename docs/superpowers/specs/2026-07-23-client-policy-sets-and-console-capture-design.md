# Client-submitted policy sets, console capture, per-policy bundles

Date: 2026-07-23

## Problem

Three related gaps, all surfaced by wanting clients to choose which Anubis
rulesets a sweep runs against:

1. **Policies are baked into the binary.** `chromesweep/policies/*.yaml` is
   `go:embed`-ed and loaded by `mustLoadPolicies()` into
   `DefaultConfig().Policies`. Changing the policy set means rebuilding and
   redeploying gubald. A client cannot ask for a different set.
2. **chrome-bully discards the browser's devtools console.** When a version
   fails an Anubis challenge, the JS/WASM errors that explain *why* are visible
   only inside Chrome and never reach the pod logs.
3. **The report bundle collides across policies.** Frames are namespaced by
   policy; logs are not. `logs/<browser>-<tag>-<container>.log` is identical for
   every policy, so a multi-policy sweep writes duplicate zip entries and one
   policy's logs effectively overwrite another's.
4. **The results table points at paths that do not exist.** `RenderMarkdown`
   renders `r.FramePath` (`report.go:154`), the absolute local temp path such as
   `/tmp/smoketest-frames-123/default-config-chrome-150.png`. In a bundle
   someone downloads — or a PR comment — that path resolves to nothing.

Gap 3 is latent today but becomes routine once clients submit multi-policy sets,
and it would silently swallow the console output added by gap 2. Gaps 3 and 4
share a fix: one helper that names a result's artifacts inside the bundle.

## Design

### A. Policy sets move to `test/gubal/` and travel over the wire

**Files.** Move `chromesweep/policies/*.yaml` (`default-config`, `fast`,
`metarefresh`, `preact`) and its `README.md` to `test/gubal/`. Reword the README:
the files are no longer compiled in; they are read from disk by `chrome-sweep`
and shipped by clients. The naming constraints stay (DNS-safe, must CHALLENGE
rather than ALLOW).

**`chromesweep/policies.go`.** Drop `//go:embed` and `policyFS`. Replace
`LoadPolicies()` with two functions, both returning policies sorted by name so
report ordering stays deterministic:

```go
// LoadPoliciesFromDir reads every *.yaml in dir. Errors if dir is missing or
// contains no policies — a sweep with no rulesets is a configuration mistake,
// not a valid run.
func LoadPoliciesFromDir(dir string) ([]Policy, error)

// PoliciesFromMap converts a wire map (name -> ruleset YAML) to sorted policies.
func PoliciesFromMap(m map[string]string) []Policy
```

`Policy` itself is unchanged.

**`chromesweep/config.go`.** `DefaultConfig()` no longer sets `Policies`; delete
`mustLoadPolicies`. `Policies` defaults to nil and every caller fills it. Update
the field comment, which currently claims the rulesets come from `policies/`.

**Proto.** Add to `SmokeTestRequest`:

```proto
map<string, string> policies = 5 [
  (buf.validate.field).map.min_pairs = 1,
  (buf.validate.field).map.keys.string.pattern = "^[a-z0-9]([a-z0-9-]*[a-z0-9])?$",
  (buf.validate.field).map.keys.string.max_len = 49,
  (buf.validate.field).map.values.string.min_len = 1
];
```

The value is the full ruleset YAML, so gubald needs no policy files on disk. The
key becomes a ConfigMap named `anubis-policy-<name>`, hence the DNS-1123 label
pattern; `max_len = 49` keeps that name inside the 63-character limit. An empty
map is rejected as `invalid_argument` — there is no server-side default set.

Per-key and per-value constraints go under `map.keys` / `map.values`, mirroring
the `repeated.items` rule already documented in CLAUDE.md. Regenerate with
`buf lint && buf generate`.

**gubald.** In `executeSweep`, after `cfg := chromesweep.DefaultConfig()`:

```go
cfg.Policies = chromesweep.PoliciesFromMap(req.GetPolicies())
```

`protovalidate.Validate` at each RPC entry guarantees a non-empty, well-keyed
map before this runs. It recurses into nested messages, so `SubmitSmokeTest`
validates its embedded `test` too.

**gubalctl.** Add `-policy-dir` (default `test/gubal`). A local reader —
`loadPolicyDir(dir) (map[string]string, error)` — walks the directory and builds
the name→content map, erroring when the directory is missing or empty. The map
is attached to both the sync and async request.

This deliberately duplicates ~12 lines rather than importing `chromesweep`.
Importing that package would link client-go into gubalctl, which is a thin CI
client; the duplicated logic is a directory listing and a file read.

**chrome-sweep.** Add `-policy-dir` (default `test/gubal`) and set
`cfg.Policies` from `LoadPoliciesFromDir`. Both CLIs now fail loudly when no
policies are available, rather than silently sweeping once against whatever
ruleset Anubis happens to be running.

### B. chrome-bully forwards the devtools console

In `capture`, enable the `Runtime` and `Log` CDP domains and attach a listener
that forwards three event types to slog. Always on: the challenge page's JS and
WASM output is the diagnostic signal the tool exists to produce.

| Event | Carries |
| --- | --- |
| `runtime.EventConsoleAPICalled` | `console.log/info/warn/error/debug`, args flattened to text |
| `runtime.EventExceptionThrown` | uncaught JS exceptions: message, description, URL, line |
| `log.EventEntryAdded` | browser-level entries: network failures, CSP/security, deprecations |

Import `cdproto/log` as `cdplog` so it does not read as the stdlib logger.

All three log at `msg: "browser console"` with a `kind` field
(`console` / `exception` / `log-entry`) plus text, URL, line, and source. The
distinct `msg` matters: `capturedFramePath` (`chromesweep/parse.go:48`) scans
chrome-bully's JSON lines for `msg == "captured"` and `msg == "fatal"`, so
console lines pass through it untouched.

CDP level maps onto slog level — `error`/`assert` → Error, `warning` → Warn,
`debug`/`trace`/`verbose` → Debug, everything else → Info.

**Ordering gotcha.** `chromedp.ListenTarget` panics when the context has no
target yet, and the target is created lazily on the first `Run`. So the sequence
is: create `tabCtx`, call `chromedp.Run(tabCtx)` with no actions to initialize
the target, attach the listener, *then* run the setup actions ending in
`Navigate`. Attaching before `Navigate` is what makes early page messages
visible.

The callback runs on chromedp's event goroutine. slog is safe for concurrent
use; the handler must not block.

No new plumbing is needed to retain this output: chrome-bully's stderr is
already collected as the `chrome-bully` container log and bundled by
`sweepOne`.

### C. Per-policy bundle subfolders, and a results table that links into them

Add two methods on `Result` that name its artifacts inside the bundle, derived
from `Policy`, `Browser`, and `Tag` rather than from `filepath.Base(FramePath)`:

```go
// BundleFramePath returns the frame's path inside the report bundle, or "" when
// no frame was captured.
func (r Result) BundleFramePath() string

// BundleLogPath returns the given container log's path inside the bundle.
func (r Result) BundleLogPath(container string) string
```

They are the single source of truth for bundle layout:

```text
report.zip
  report.md
  report.json
  frames/<policy>/<browser>-<tag>.png
  logs/<policy>/<browser>-<tag>-<container>.log
```

When `Policy` is `""` (the live-policy path in `sweep.go:77`) the subfolder is
omitted and the layout matches today's flat form.

**`WriteBundle`** uses both methods for its zip entry names, replacing
`"frames/"+filepath.Base(r.FramePath)` and the `logs/%s-%s-%s.log` format
string. It still reads the file's *contents* from the local `r.FramePath`.

**`RenderMarkdown`** renders `dash(r.BundleFramePath())` in the frame column
instead of `dash(r.FramePath)`. The table then names a path that actually exists
next to the `report.md` the reader is looking at.

The on-disk `framesDir` layout is unchanged: `localFrameName` already produces
the collision-free `<policy>-<browser>-<tag>.png`, and keeping it flat avoids
new directory creation in `CopyFrame`'s path. Only the zip, which is the
artifact humans browse, gains structure.

**Non-goal.** `report.json` keeps `frame_path` as the local absolute path.
`WriteBundle` needs it to read the file, nothing in the repo reads the field
back, and changing its meaning is a schema change better made on its own. It is
worth revisiting later, since that path is equally meaningless to anyone
outside the sweep machine.

## Testing

- `chromesweep/policies_test.go`: rewrite against `LoadPoliciesFromDir` using a
  temp dir — sorting, `.yaml` filtering, missing dir, empty dir. Add
  `PoliciesFromMap` cases for sorting and the empty map.
- `chromesweep/config_test.go`: `TestDefaultConfigLoadsPolicies` (line 48)
  asserts `len(cfg.Policies) >= 2` and must go — `DefaultConfig().Policies` is
  now nil by design.
- `chromesweep/bundle_test.go`: existing assertions use flat names
  (`frames/150.png`, `logs/firefox-152-chrome-bully.log`) and need updating to
  the per-policy paths. Add the case this change exists for: two policies
  covering the same browser+tag produce distinct log entries rather than
  colliding.
- `chromesweep/report_test.go`: assert the frame column renders the
  bundle-relative path, and that `BundleFramePath`/`BundleLogPath` omit the
  subfolder when `Policy` is empty and return `""` for a missing frame.
- `cmd/gubald/svc/smoketest`: extend the protovalidate test with an empty map
  (rejected), a bad key (rejected), and a valid map (accepted). These rules
  compile at first validation, so only a test catches a malformed rule.
- `cmd/gubalctl`: test `loadPolicyDir` for the map it builds and its errors on
  missing and empty directories.
- `cmd/chrome-bully`: unit-test the CDP-level→slog-level mapping and the console
  arg flattening. The listener wiring itself needs a real browser and stays
  uncovered.

## Consequences

- gubald no longer ships or embeds policy YAML; the client owns the set. A
  client that sends nothing gets a clear `invalid_argument` instead of a silent
  default.
- `test/gubal/` becomes the source of truth, matching the repo's `test/<name>/`
  convention, and editing a ruleset no longer requires a rebuild.
- Console capture increases chrome-bully log volume. That volume lands in the
  bundle, which is where it is useful.
- Existing bundle consumers that expect flat `frames/` and `logs/` paths will
  need updating.
- The results table's frame column changes from an absolute temp path to a
  bundle-relative one. Anyone reading `report.md` — in the zip or in a PR
  comment — can now find the referenced frame.
