# Firefox Sweep in `chromesweep` — Design

## Problem

`chromesweep` tests a list of **Chrome** image tags against an in-cluster Anubis
setup. The proto already carries `firefox_versions` alongside `chrome_versions`,
and Firefox era-images exist (`ghcr.io/techarohq/gubal/firefox:152`, also tagged
by major like Chrome). We want a single smoke-test run to sweep **both** browsers,
sharing the one Anubis re-image and the one collector pod, and report each
browser's results separately.

`chrome-bully` already drives the Firefox image over CDP and captures frames; that
is out of scope here. This work is purely about wiring `chromesweep` to stand up
and tear down a Firefox target in Kubernetes and to run it as a second sweep.

## Goals

- One `Run` sweeps every configured browser, setting up Anubis + the collector
  **once**.
- Firefox gets its own per-version resources (`firefox-<tag>` Deployment/Service/
  `firefox-<tag>-lockdown` NetworkPolicy, `firefox-smoke-<tag>` Job) from Firefox
  manifests under `k8s/firefox/`.
- The report shows a Chrome section and a Firefox section.
- gubald maps `chrome_versions` + `firefox_versions` into one run; the CLI can
  drive either or both.

## Non-goals

- Changing `chrome-bully` or how frames are captured/detected.
- Parallelizing the two browsers against each other (they run sequentially; each
  browser's versions still run in a bounded pool).
- Any change to the Anubis re-image / restore logic beyond running it once.

## Design

### 1. Split `Config` into run-wide settings + per-browser targets

`Config` currently mixes run-wide settings with Chrome-specific target fields.
Extract the target into a `Browser`:

```go
// Browser describes one browser target to sweep.
type Browser struct {
    Name                  string // "chrome" / "firefox" — report section, frame prefix, resource base
    ImageRepo             string // ghcr.io/techarohq/gubal/{chrome,firefox}
    Deployment            string // base name → <name>-<tag> resources; also the CDP host rewritten in the Job
    Container             string // container within the Deployment to re-image
    JobName               string // "chrome-smoke" / "firefox-smoke"
    DeploymentManifest    string
    ServiceManifest       string
    NetworkPolicyManifest string
    JobManifest           string
    Versions              []string
}

func ChromeBrowser() Browser  // k8s/deployment.yaml … , repo .../chrome, names "chrome"
func FirefoxBrowser() Browser // k8s/firefox/deployment.yaml … , repo .../firefox, names "firefox"
```

`Config` keeps the run-wide fields (`Namespace`, `AnubisManifest`,
`AnubisContainer`, `AnubisImage`, `Parallelism`, `CollectorPVC`, `OutDir`,
`ReadyTimeout`, `JobTimeout`) and gains `Browsers []Browser`. The per-browser
fields (`Deployment`, `Container`, `ImageRepo`, `JobName`, the four manifest
paths, `Versions`) move **off** `Config` onto `Browser`.

`DefaultConfig()` returns the run-wide defaults with
`Browsers: []Browser{ChromeBrowser(), FirefoxBrowser()}`, so the natural default
sweeps both browsers. Callers override `Browsers` (and each browser's `Versions`).

**Default version lists** live on the presets, so a no-flags CLI run and any
`DefaultConfig` consumer sweep sensible sets:

- `ChromeBrowser().Versions = 75,80,85,90,95,100,105,110,115,120,125,130,135,140,145,150`
- `FirefoxBrowser().Versions = 129,135,140,145,150,152`

### 2. `Run` sets up shared infra once, then sweeps each browser

`Run(ctx, c, cfg, framesDir)` keeps its signature — `Browsers` rides inside
`cfg`. Flow:

1. `prepareAnubis` — **once**.
2. `EnsureCollector` — **once**.
3. For each `browser` in `cfg.Browsers` (sequentially): load that browser's base
   manifests, then sweep its `Versions` in a bounded pool of `cfg.Parallelism`.

`sweepOne` takes a `Browser` (and the shared run-wide `cfg`) instead of reading
Chrome fields off `Config`. `loadBaseManifests` takes a `Browser`. `retargetJob`
already rewrites `<Deployment>:9222 → <Deployment>-<tag>:9222`, so it works
unchanged with `Browser.Deployment == "firefox"`.

Results across all browsers accumulate into one flat `[]Result`, each tagged with
its browser, preserving browser-then-version order.

### 3. Results tagged by browser; two-section report

`Result` gains `Browser string`, and `ChromeVersion` is renamed to
`BrowserVersion` (it always held the browser-detected version; the name was
Chrome-specific). Proto field `chrome_version` → `browser_version`, **keeping
field number 3** so the change is wire-compatible.

**Request validation stays strict.** A smoke-test request must include **both**
`chrome_versions` and `firefox_versions` (`repeated.min_items = 1` on each, as
committed). We do *not* relax this. The current failure is a stale test: the
`valid` case in `smoketest_test.go` omits `firefox_versions` and so trips
`min_items`. Fix the test (add Firefox versions to `valid`; add Firefox-specific
cases), not the proto rule. `main` is red on this today.

`RenderMarkdown` groups results by browser in first-seen order and emits one
section per browser:

```
# Chrome version sweep — 4/4 passed

Anubis image: `…`

| tag | status | browser version | frame | detail |
| … |

# Firefox version sweep — 3/4 passed

| tag | status | browser version | frame | detail |
| … |
```

`AllPassed` is unchanged (any non-pass fails the whole run).

**Frame collision fix.** Chrome 130 and Firefox 130 are both real tags. Today the
local frame file and the zip entry key on tag alone (`130.png`) and would clobber
across browsers. Namespace them `<browser>-<tag>.png` (e.g. `chrome-130.png`,
`firefox-130.png`). This changes the local filename in `sweepOne`; `WriteBundle`
already keys on `filepath.Base(r.FramePath)`, so it inherits the namespaced name.

### 4. Firefox k8s manifests under `k8s/firefox/`

Mirror the four Chrome manifests, names swapped to `firefox` / `firefox-smoke`,
image repo `ghcr.io/techarohq/gubal/firefox`, CDP host `firefox:9222`:

- `k8s/firefox/deployment.yaml` — name `firefox`, `app: firefox`, container
  `firefox` image `firefox`, CDP port 9222, Kata runtimeClass, `fsGroup: 993`,
  `dshm` volume, readiness/liveness on `/json/version` with `Host: localhost:9222`.
- `k8s/firefox/service.yaml` — `firefox`, selector `app: firefox`, port 9222.
- `k8s/firefox/networkpolicy.yaml` — `firefox-lockdown`, podSelector
  `app: firefox`, ingress from `role: chrome-controller` on 9222, egress to DNS +
  `app: anubis`.
- `k8s/firefox/smoke-job.yaml` — `firefox-smoke`, pod label
  `role: chrome-controller` (reused so the NetworkPolicy and Chrome manifests
  stay untouched), `smoke` container asserting a Firefox UA, `chrome-bully`
  container with `-cdp-url=http://firefox:9222`, same `chrome-bully-data` PVC.

Two Firefox-specific manifest details:

- **UA assertion.** Firefox headless does not leak `Headless` in its UA (that is a
  Chrome behavior). The `smoke` container asserts the UA contains `Firefox/`
  (driveable); the real non-headless proof is `chrome-bully` transiting Anubis.
- **Controller label.** The Firefox smoke pod keeps `role: chrome-controller` and
  the Firefox NetworkPolicy allows that same label. Reusing it avoids editing the
  Chrome manifests; the label is an arbitrary selector, not Chrome-specific state.

**Assumption to verify against the real `firefox:152` image:** its CDP endpoint
serves `/json/version` returning a JSON body with a `Firefox/` User-Agent, same
shape as Chrome DevTools. If the endpoint differs, the `smoke` container's curl /
assertion is adapted to match; `chrome-bully` (which the user confirmed works) is
unaffected.

### 5. Callers

**gubald** (`cmd/gubald/svc/smoketest`): build the run-wide `cfg`, set
`AnubisImage`, then assemble `cfg.Browsers` from the request — `ChromeBrowser()`
with the request's `chrome_versions` and `FirefoxBrowser()` with its
`firefox_versions`. Validation guarantees both are present, but the assembly
appends whichever are non-empty so it degrades safely. One `Run`. Map each
`Result` to a proto result, adding a `browser` field (proto field 6 on the
result message) so the flat `results` list is disambiguable; `browser_version`
(field 3) carries the detected version.

**gubalctl** (`cmd/gubalctl`): add a `-firefox-versions` flag mirroring
`-chrome-versions`, defaulted to the Firefox list above (and default
`-chrome-versions` to the Chrome list). Parse both and send them in the request.

**chrome-sweep CLI** (`cmd/chrome-sweep`): replace the positional-versions +
per-browser override flags with:

- `-chrome-versions 150,151` (comma list)
- `-firefox-versions 152,140` (comma list)

Both default to the preset version lists (Chrome + Firefox) when their flag is
empty, so a no-flags invocation sweeps both. Whichever resolve non-empty are
swept (both → both sweeps in one run). Run-wide flags
(`-namespace`, `-anubis-manifest`, `-anubis-container`, `-anubis-image`,
`-parallelism`, `-pvc`, `-out`, `-ready-timeout`, `-job-timeout`) stay. The
removed per-browser override flags (`-deployment`, `-container`, `-image-repo`,
`-job-name`, `-*-manifest`) become preset-driven. **This is a breaking change to
an internal CLI.** Error if neither versions flag is set.

### 6. Testing

Per repo convention, unit-test the pure pieces; the browser loop in `Run` stays
real-cluster-only.

- `config_test.go` — `ChromeBrowser()` / `FirefoxBrowser()` presets carry the
  expected names/repos/manifest paths; `DefaultConfig()` defaults to Chrome.
- `report_test.go` — two-section grouping (browser-ordered headers, per-section
  pass counts, `browser version` column) and single-browser back-compat.
- `sweep_test.go` (pure helper) — browser-namespaced frame filename
  (`<browser>-<tag>.png`) and `versionFromFrame` unchanged.
- `retarget_test.go` — `retargetJob` against `firefox:9222` rewrites to
  `firefox-<tag>:9222` and leaves `Host: localhost:9222` alone.
- gubald `smoketest_test.go` — request with both version lists maps to two
  browsers; results carry `browser` + `browser_version`; protovalidate still
  compiles.
- Loader smoke test decodes the real `k8s/firefox/*.yaml`.

## Files

- `chromesweep/config.go` — MODIFY: `Browser` type, `ChromeBrowser`/
  `FirefoxBrowser` presets, `Config` reshaped (run-wide + `Browsers`),
  `DefaultConfig` updated.
- `chromesweep/sweep.go` — MODIFY: `Run` iterates browsers with shared
  Anubis/collector; `sweepOne` / `loadBaseManifests` take a `Browser`;
  browser-namespaced frame filename; `Result.Browser` set.
- `chromesweep/report.go` — MODIFY: `Result.Browser`, `ChromeVersion` →
  `BrowserVersion`, two-section `RenderMarkdown`.
- `chromesweep/retarget.go` — no change expected (already `Deployment`-parameterized);
  confirm via test.
- `k8s/firefox/{deployment,service,networkpolicy,smoke-job}.yaml` — NEW.
- `pb/techaro/lol/gubal/v1/gubal.proto` — MODIFY: `chrome_version`→`browser_version`
  (keep field 3), add `browser` (field 6) to the result message; regenerate `gen/`.
  Request `min_items` rules unchanged.
- `cmd/gubald/svc/smoketest/smoketest.go` — MODIFY: assemble `cfg.Browsers` from
  both version lists; map `Browser`/`BrowserVersion`.
- `cmd/gubald/svc/smoketest/smoketest_test.go` — MODIFY: fix `valid` case to
  include `firefox_versions`; add Firefox validation cases.
- `cmd/chrome-sweep/main.go` — MODIFY: `-chrome-versions`/`-firefox-versions`
  flags defaulting to preset lists; drop per-browser override flags; build
  `cfg.Browsers`.
- `cmd/gubalctl/main.go` — MODIFY: add `-firefox-versions` flag (defaulted),
  default `-chrome-versions`, send both in the request.
- Tests as listed in §6.

## Risks

- **Firefox CDP `/json/version` shape** (see §4 assumption) — the only real
  unknown; verified against the live image during implementation.
- **Breaking CLI flags** — acceptable for an internal tool; documented in the
  CLI's README.
- **Proto field rename** — wire-compatible (field numbers preserved); regenerated
  code and gubald mapping updated together.
