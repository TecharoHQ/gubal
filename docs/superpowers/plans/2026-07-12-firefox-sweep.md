# Firefox Sweep in chromesweep — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make one smoke-test run sweep **both** Chrome and Firefox: `chromesweep.Run` sets up Anubis + the collector once, then sweeps each configured browser's versions against per-version k8s resources, and reports a Chrome section and a Firefox section.

**Architecture:** Extract the per-browser target fields off `Config` into a `Browser` struct with `ChromeBrowser()`/`FirefoxBrowser()` presets (each carrying default version lists and its k8s manifest paths). `Config` keeps run-wide settings plus `Browsers []Browser`. `Run` iterates browsers sharing one Anubis re-image and one collector pod; each `Result` is tagged with its browser and the report renders per-browser sections. Firefox gets its own manifests under `k8s/firefox/`. gubald, gubalctl, and the chrome-sweep CLI are wired to drive both browsers.

**Tech Stack:** Go 1.26.4, `k8s.io/client-go` v0.31.1 (typed clients + fake clientset), `sigs.k8s.io/yaml` v1.4.0, `buf` (protobuf/Twirp + protovalidate). No new dependencies.

## Global Constraints

Copy these exact values; every task depends on them.

- Go module path: `github.com/TecharoHQ/gubal`; Go `go 1.26.4`.
- Build binaries into `./var`, never the repo root (e.g. `go build -o ./var/chrome-sweep ./cmd/chrome-sweep`).
- Kubernetes namespace: `ci`.
- Browser presets (base resource name, container, image repo, job name, manifests):
  - **chrome**: name `chrome`, container `chrome`, repo `ghcr.io/techarohq/gubal/chrome`, job `chrome-smoke`, manifests `k8s/deployment.yaml` / `k8s/service.yaml` / `k8s/networkpolicy.yaml` / `k8s/smoke-job.yaml`.
  - **firefox**: name `firefox`, container `firefox`, repo `ghcr.io/techarohq/gubal/firefox`, job `firefox-smoke`, manifests `k8s/firefox/deployment.yaml` / `k8s/firefox/service.yaml` / `k8s/firefox/networkpolicy.yaml` / `k8s/firefox/smoke-job.yaml`.
- Default version lists (comma-form):
  - chrome: `75,80,85,90,95,100,105,110,115,120,125,130,135,140,145,150`
  - firefox: `129,135,140,145,150,152`
- Per version `<tag>` for a browser with base name `<b>`, all in `ci`: Deployment `<b>-<tag>` (labels/selector `app: <b>-<tag>`, container re-imaged to `<repo>:<tag>`), Service `<b>-<tag>`, NetworkPolicy `<b>-<tag>-lockdown`, Job `<jobName>-<tag>` with CDP host rewritten from `<b>:9222` to `<b>-<tag>:9222` (leave `Host: localhost:9222` untouched).
- Shared / not templated: Anubis (`k8s/anubis/anubis.yaml`, container `anubis`), one collector pod, PVC `chrome-bully-data`. Anubis is re-imaged once per run and restored after.
- Captured frame local filename is namespaced by browser: `<browser>-<tag>.png` (Chrome 130 and Firefox 130 both exist — bare `<tag>.png` collides).
- Request validation is strict: a `SmokeTestRequest` MUST include ≥1 `chrome_versions` AND ≥1 `firefox_versions` (unchanged `repeated.min_items = 1`).
- `chrome-bully` logs the frame as `{"level":"INFO","msg":"captured","path":"/data/chrome-<version>-<ts>.png"}` (the on-PVC name always starts `chrome-` regardless of browser); the `smoke` container logs `User-Agent: <ua>`.
- Default parallelism: `8`.
- CWD at runtime is the repo root (presets use `k8s/...` paths); package tests run from `chromesweep/` and load manifests via `../k8s/...`.
- Commit style: Conventional Commits (`feat(...)`, `refactor(...)`, `test(...)`, `docs(...)`, `chore(gen)`).

**Current state:** `main` is red — `go test ./cmd/gubald/svc/smoketest/` fails because the `valid` test omits `firefox_versions` and trips `min_items`. Task 4 fixes this.

---

## File Structure

- `k8s/firefox/deployment.yaml`, `service.yaml`, `networkpolicy.yaml`, `smoke-job.yaml` — NEW. Firefox mirrors of the base Chrome manifests.
- `chromesweep/config.go` — MODIFY. Add `Browser` type + `ChromeBrowser`/`FirefoxBrowser` presets; reshape `Config` (run-wide fields + `Browsers`); update `DefaultConfig`.
- `chromesweep/config_test.go` — NEW. Preset values, default version lists, `DefaultConfig` shape; decode-smoke for `k8s/firefox/*.yaml`.
- `chromesweep/report.go` — MODIFY. `Result.Browser`; `ChromeVersion`→`BrowserVersion`; two-section `RenderMarkdown`.
- `chromesweep/report_test.go` — MODIFY. Update field names; add two-section grouping test.
- `chromesweep/sweep.go` — MODIFY. `Run` iterates `cfg.Browsers` with shared Anubis/collector; `sweepOne`/`loadBaseManifests` take a `Browser`; `localFrameName` helper; set `Result.Browser`.
- `chromesweep/sweep_test.go` — NEW. `localFrameName` unit test.
- `chromesweep/retarget_test.go` — MODIFY. Add a Firefox-host `retargetJob` case.
- `pb/techaro/lol/gubal/v1/gubal.proto` — MODIFY. Rename result field 3 `chrome_version`→`browser_version`; add field 6 `browser`.
- `gen/techaro/lol/gubal/v1/*` — REGENERATE via `buf generate`.
- `cmd/gubald/svc/smoketest/smoketest.go` — MODIFY. `browsersFor` helper; assemble `cfg.Browsers`; map `Browser`/`BrowserVersion`.
- `cmd/gubald/svc/smoketest/smoketest_test.go` — MODIFY. Fix `valid` case; add Firefox cases; add `browsersFor` test.
- `cmd/chrome-sweep/main.go` — MODIFY. `-chrome-versions`/`-firefox-versions` flags (default to preset lists); drop per-browser override flags; build `cfg.Browsers`.
- `cmd/gubalctl/main.go` — MODIFY. Add `-firefox-versions` (defaulted); default `-chrome-versions`; send both.

The pure pieces (presets, report grouping, `localFrameName`, `retargetJob`, `browsersFor`, request validation) are the testable core. `Run`'s browser loop and real frame copy stay real-cluster-only (final task documents the manual run).

---

### Task 1: Firefox k8s manifests

**Files:**
- Create: `k8s/firefox/deployment.yaml`, `k8s/firefox/service.yaml`, `k8s/firefox/networkpolicy.yaml`, `k8s/firefox/smoke-job.yaml`
- Test: (decode smoke test added in Task 2's `config_test.go`; here, validate by YAML round-trip)

**Interfaces:**
- Consumes: nothing.
- Produces: four manifests that `chromesweep`'s existing `loadDeployment`/`loadService`/`loadNetworkPolicy`/`loadJob` can decode, named `firefox` / `firefox-smoke`, CDP host `firefox:9222`.

- [ ] **Step 1: Write `k8s/firefox/deployment.yaml`**

```yaml
# Long-running headless Firefox as a CDP server (chrome-bully/chromedp compatible),
# isolated with Kata Containers. Mirror of k8s/deployment.yaml with the image and
# names swapped to firefox. Swap the image tag to prove a different Firefox version.
apiVersion: apps/v1
kind: Deployment
metadata:
  name: firefox
  labels: { app: firefox }
spec:
  replicas: 1
  selector:
    matchLabels: { app: firefox }
  template:
    metadata:
      labels: { app: firefox }
    spec:
      # Kata is the real trust boundary here (a VM per pod). Ancient browsers' own
      # sandboxes have unpatched escape CVEs, so we lean on Kata instead.
      runtimeClassName: kata
      automountServiceAccountToken: false
      securityContext:
        seccompProfile:
          type: RuntimeDefault
        # Own the chrome-bully PVC as gid 993 so its non-root user can write frames.
        fsGroup: 993
      containers:
        - name: firefox
          image: firefox
          imagePullPolicy: IfNotPresent
          ports:
            - name: cdp
              containerPort: 9222
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
          resources:
            requests: { cpu: "500m", memory: "512Mi" }
            limits: { cpu: "2", memory: "2Gi" }
          # DevTools rejects DNS-name Host headers; force an accepted one.
          readinessProbe:
            httpGet:
              path: /json/version
              port: cdp
              httpHeaders: [{ name: Host, value: "localhost:9222" }]
            initialDelaySeconds: 3
            periodSeconds: 5
          livenessProbe:
            httpGet:
              path: /json/version
              port: cdp
              httpHeaders: [{ name: Host, value: "localhost:9222" }]
            initialDelaySeconds: 10
            periodSeconds: 15
          volumeMounts:
            - { name: dshm, mountPath: /dev/shm }
      volumes:
        - name: dshm
          emptyDir:
            medium: Memory
            sizeLimit: 1Gi
```

- [ ] **Step 2: Write `k8s/firefox/service.yaml`**

```yaml
apiVersion: v1
kind: Service
metadata:
  name: firefox
  labels: { app: firefox }
spec:
  selector: { app: firefox }
  ports:
    - name: cdp
      port: 9222
      targetPort: cdp
```

- [ ] **Step 3: Write `k8s/firefox/networkpolicy.yaml`**

```yaml
# CDP on port 9222 is unauthenticated *full control* of the browser. Lock it down so
# only the controller pod can reach it, and so Firefox can't phone home from the pod.
# Reuses the role=chrome-controller label (the smoke Job carries it) so the base
# Chrome manifests stay untouched.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: firefox-lockdown
spec:
  podSelector:
    matchLabels: { app: firefox }
  policyTypes: ["Ingress", "Egress"]
  ingress:
    - from:
        - podSelector:
            matchLabels: { role: chrome-controller }
      ports:
        - { protocol: TCP, port: 9222 }
  egress:
    - to: []
      ports:
        - { protocol: UDP, port: 53 }
        - { protocol: TCP, port: 53 }
    - to:
        - podSelector:
            matchLabels: { app: anubis }
      ports:
        - { protocol: TCP, port: 8443 }
        - { protocol: TCP, port: 443 }
```

- [ ] **Step 4: Write `k8s/firefox/smoke-job.yaml`**

Mirror of `k8s/smoke-job.yaml`; CDP host `firefox:9222`; the `smoke` container asserts a Firefox UA (Firefox headless does not leak `Headless`, so the real non-headless proof is `chrome-bully` transiting Anubis); the marker header is `X-Firefox-Bully`.

```yaml
# One-shot proving Job for Firefox: connect to the Firefox CDP Service, print
# /json/version, assert the reported User-Agent looks like real Firefox, then drive
# it through Anubis with chrome-bully. Mirror of k8s/smoke-job.yaml.
#
# Carries role=chrome-controller so the firefox NetworkPolicy lets it reach 9222.
apiVersion: batch/v1
kind: Job
metadata:
  name: firefox-smoke
spec:
  backoffLimit: 2
  ttlSecondsAfterFinished: 300
  template:
    metadata:
      labels: { role: chrome-controller }
    spec:
      restartPolicy: Never
      automountServiceAccountToken: false
      containers:
        - name: smoke
          image: curlimages/curl:8.8.0
          command: ["/bin/sh", "-c"]
          args:
            - |
              set -eu
              # DevTools rejects DNS-name Host headers (anti DNS-rebinding), so we
              # reach the Service by name but send an accepted Host header.
              echo "waiting for firefox:9222 ..."
              for i in $(seq 1 30); do
                if curl -fsS -H "Host: localhost:9222" \
                     "http://firefox:9222/json/version" -o /tmp/v.json; then break; fi
                sleep 2
              done
              cat /tmp/v.json; echo
              ua=$(sed -n 's/.*"User-Agent": *"\([^"]*\)".*/\1/p' /tmp/v.json)
              echo "User-Agent: ${ua}"
              case "${ua}" in
                *Firefox/*) echo "PASS: driveable Firefox UA" ;;
                *)          echo "FAIL: unexpected/empty User-Agent"; exit 1 ;;
              esac

              # Anubis is up and serving its challenge.
              echo "checking Anubis at anubis-ci.alrest.cetacean.club ..."
              code=$(curl -sS -o /tmp/anubis.html -H "User-Agent: Mozilla" -w '%{http_code}' \
                       https://anubis-ci.alrest.cetacean.club/ || echo 000)
              echo "Anubis HTTP ${code}"
              [ "${code}" = "200" ] || { echo "FAIL: Anubis not 200"; exit 1; }
              grep -qi anubis /tmp/anubis.html \
                && echo "PASS: Anubis is serving its challenge" \
                || { echo "FAIL: response is not Anubis"; exit 1; }
        # Controller: drives Firefox over CDP (Host "localhost" satisfies the
        # DNS-rebind check) through Anubis, sets a marker header, waits for it to
        # echo back, then screenshots. Frames land on the shared PVC.
        - name: chrome-bully
          image: ghcr.io/techarohq/gubal/chrome-bully:latest
          imagePullPolicy: Always
          args:
            - -cdp-url=http://firefox:9222
            - -target-url=https://anubis-ci.alrest.cetacean.club
            - "-header=X-Firefox-Bully: firefox-bully-roundtrip-ok"
            - -expect-text=firefox-bully-roundtrip-ok
            - -capture-timeout=90s
            - -out-dir=/data
          securityContext:
            allowPrivilegeEscalation: false
            runAsUser: 997
            runAsGroup: 993
            capabilities:
              drop: ["ALL"]
          resources:
            requests: { cpu: "50m", memory: "64Mi" }
            limits: { cpu: "500m", memory: "256Mi" }
          volumeMounts:
            - { name: bully-data, mountPath: /data }
      volumes:
        - name: bully-data
          persistentVolumeClaim:
            claimName: chrome-bully-data
```

- [ ] **Step 5: Verify the YAML parses**

Run: `for f in k8s/firefox/*.yaml; do echo "== $f"; go run sigs.k8s.io/yaml/... </dev/null 2>/dev/null; python3 -c "import sys,yaml; yaml.safe_load(open('$f'))" && echo OK; done`
Expected: each prints `OK` (valid YAML). If `python3`/`yaml` is unavailable, defer verification to Task 2's decode-smoke test.

- [ ] **Step 6: Commit**

```bash
git add k8s/firefox/
git commit -m "feat(firefox): add per-version Firefox k8s manifests"
```

---

### Task 2: `Browser` type, presets, and reshaped `Config`

**Files:**
- Modify: `chromesweep/config.go`
- Test: `chromesweep/config_test.go` (new)

**Interfaces:**
- Consumes: the Task 1 manifests (by path string; and decoded in the smoke test).
- Produces:
  - `type Browser struct { Name, ImageRepo, Deployment, Container, JobName, DeploymentManifest, ServiceManifest, NetworkPolicyManifest, JobManifest string; Versions []string }`
  - `func ChromeBrowser() Browser`
  - `func FirefoxBrowser() Browser`
  - `type Config struct { Namespace, AnubisManifest, AnubisContainer, AnubisImage string; Parallelism int; CollectorPVC, OutDir string; ReadyTimeout, JobTimeout time.Duration; Browsers []Browser }`
  - `func DefaultConfig() Config`

- [ ] **Step 1: Write the failing test** — `chromesweep/config_test.go`

```go
package chromesweep

import (
	"strings"
	"testing"
)

func TestBrowserPresets(t *testing.T) {
	c := ChromeBrowser()
	if c.Name != "chrome" || c.Container != "chrome" || c.Deployment != "chrome" || c.JobName != "chrome-smoke" {
		t.Fatalf("chrome preset names wrong: %+v", c)
	}
	if c.ImageRepo != "ghcr.io/techarohq/gubal/chrome" {
		t.Fatalf("chrome repo = %q", c.ImageRepo)
	}
	if c.DeploymentManifest != "k8s/deployment.yaml" || c.JobManifest != "k8s/smoke-job.yaml" {
		t.Fatalf("chrome manifests wrong: %+v", c)
	}
	if strings.Join(c.Versions, ",") != "75,80,85,90,95,100,105,110,115,120,125,130,135,140,145,150" {
		t.Fatalf("chrome default versions = %v", c.Versions)
	}

	f := FirefoxBrowser()
	if f.Name != "firefox" || f.Container != "firefox" || f.Deployment != "firefox" || f.JobName != "firefox-smoke" {
		t.Fatalf("firefox preset names wrong: %+v", f)
	}
	if f.ImageRepo != "ghcr.io/techarohq/gubal/firefox" {
		t.Fatalf("firefox repo = %q", f.ImageRepo)
	}
	if f.DeploymentManifest != "k8s/firefox/deployment.yaml" || f.JobManifest != "k8s/firefox/smoke-job.yaml" {
		t.Fatalf("firefox manifests wrong: %+v", f)
	}
	if strings.Join(f.Versions, ",") != "129,135,140,145,150,152" {
		t.Fatalf("firefox default versions = %v", f.Versions)
	}
}

func TestDefaultConfigSweepsBothBrowsers(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Namespace != "ci" || cfg.Parallelism != 8 || cfg.CollectorPVC != "chrome-bully-data" {
		t.Fatalf("run-wide defaults wrong: %+v", cfg)
	}
	if len(cfg.Browsers) != 2 || cfg.Browsers[0].Name != "chrome" || cfg.Browsers[1].Name != "firefox" {
		t.Fatalf("DefaultConfig browsers = %+v", cfg.Browsers)
	}
}

// TestLoadFirefoxManifests decodes the real k8s/firefox/*.yaml so a malformed
// manifest fails here rather than at cluster time.
func TestLoadFirefoxManifests(t *testing.T) {
	if _, err := loadDeployment("../k8s/firefox/deployment.yaml", "ci"); err != nil {
		t.Fatalf("firefox deployment: %v", err)
	}
	if _, err := loadService("../k8s/firefox/service.yaml", "ci"); err != nil {
		t.Fatalf("firefox service: %v", err)
	}
	if _, err := loadNetworkPolicy("../k8s/firefox/networkpolicy.yaml", "ci"); err != nil {
		t.Fatalf("firefox networkpolicy: %v", err)
	}
	if _, err := loadJob("../k8s/firefox/smoke-job.yaml", "ci"); err != nil {
		t.Fatalf("firefox job: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails to compile**

Run: `go test ./chromesweep/ -run 'TestBrowserPresets|TestDefaultConfigSweepsBothBrowsers|TestLoadFirefoxManifests'`
Expected: build failure — `ChromeBrowser`/`FirefoxBrowser` undefined, `Config` fields removed. (This is expected; the whole package won't build until Step 3.)

- [ ] **Step 3: Rewrite `chromesweep/config.go`**

```go
// Package chromesweep tests lists of browser (Chrome and Firefox) image tags
// against an in-cluster Anubis setup: for each browser it stands up per-version
// Deployment/Service/NetworkPolicy and a smoke Job, runs it, records pass/fail plus
// a captured screenshot, then tears the version's resources down. Anubis and the
// frame collector are set up once per run and shared across browsers. It is
// importable so services (not just the CLI) can drive a sweep.
package chromesweep

import "time"

// Browser describes one browser target to sweep: its image repo, the base names
// for its per-version resources, the manifests to template, and the versions.
type Browser struct {
	Name                  string   // "chrome" / "firefox": report section, frame prefix, resource base
	ImageRepo             string   // final image ref is <ImageRepo>:<tag>
	Deployment            string   // base name -> <name>-<tag> resources; also the CDP host in the Job
	Container             string   // container within the Deployment to re-image
	JobName               string   // base name for per-version smoke Jobs
	DeploymentManifest    string
	ServiceManifest       string
	NetworkPolicyManifest string
	JobManifest           string
	Versions              []string
}

// ChromeBrowser returns the Chrome target preset (base k8s/*.yaml manifests).
func ChromeBrowser() Browser {
	return Browser{
		Name:                  "chrome",
		ImageRepo:             "ghcr.io/techarohq/gubal/chrome",
		Deployment:            "chrome",
		Container:             "chrome",
		JobName:               "chrome-smoke",
		DeploymentManifest:    "k8s/deployment.yaml",
		ServiceManifest:       "k8s/service.yaml",
		NetworkPolicyManifest: "k8s/networkpolicy.yaml",
		JobManifest:           "k8s/smoke-job.yaml",
		Versions:              []string{"75", "80", "85", "90", "95", "100", "105", "110", "115", "120", "125", "130", "135", "140", "145", "150"},
	}
}

// FirefoxBrowser returns the Firefox target preset (k8s/firefox/*.yaml manifests).
func FirefoxBrowser() Browser {
	return Browser{
		Name:                  "firefox",
		ImageRepo:             "ghcr.io/techarohq/gubal/firefox",
		Deployment:            "firefox",
		Container:             "firefox",
		JobName:               "firefox-smoke",
		DeploymentManifest:    "k8s/firefox/deployment.yaml",
		ServiceManifest:       "k8s/firefox/service.yaml",
		NetworkPolicyManifest: "k8s/firefox/networkpolicy.yaml",
		JobManifest:           "k8s/firefox/smoke-job.yaml",
		Versions:              []string{"129", "135", "140", "145", "150", "152"},
	}
}

// Config is the fully-resolved run-wide configuration for a sweep. Per-browser
// target details live on each Browser in Browsers.
type Config struct {
	Namespace       string
	AnubisManifest  string
	AnubisContainer string
	AnubisImage     string
	Parallelism     int
	CollectorPVC    string
	OutDir          string
	ReadyTimeout    time.Duration
	JobTimeout      time.Duration
	Browsers        []Browser
}

// DefaultConfig returns a Config with the standard defaults filled in, sweeping
// both Chrome and Firefox with their preset version lists. Callers usually set
// AnubisImage and override Browsers (and each browser's Versions).
func DefaultConfig() Config {
	return Config{
		Namespace:       "ci",
		AnubisManifest:  "k8s/anubis/anubis.yaml",
		AnubisContainer: "anubis",
		Parallelism:     8,
		CollectorPVC:    "chrome-bully-data",
		OutDir:          "./var/sweep",
		ReadyTimeout:    3 * time.Minute,
		JobTimeout:      4 * time.Minute,
		Browsers:        []Browser{ChromeBrowser(), FirefoxBrowser()},
	}
}
```

Note: this removes `Config.Deployment`, `Container`, `ImageRepo`, `JobManifest`, `JobName`, `DeploymentManifest`, `ServiceManifest`, `NetworkPolicyManifest`, `Versions`. `sweep.go` (Task 3) and the callers (Tasks 5–7) reference these and will not build until updated. Ignore package-wide build errors until Task 3; this task's gate is `go test ./chromesweep/ -run 'TestBrowserPresets|TestDefaultConfigSweepsBothBrowsers|TestLoadFirefoxManifests'` after Task 3 makes the package compile. **To keep Task 2 independently green, do Step 4 (the `sweep.go` compile fix) as part of this task.**

- [ ] **Step 4: Make the package compile — update `sweep.go` and `report.go` field references**

This task and Task 3 together reshape the package. To land a green gate here, apply the full `sweep.go` rewrite and `report.go` changes from Task 3 now, then run the combined tests. (If executing task-by-task with a strict one-file rule, treat Tasks 2+3 as a single review unit.)

Proceed to Task 3's steps, then return here to run:

Run: `go test ./chromesweep/`
Expected: PASS (all chromesweep tests, including the new config tests).

- [ ] **Step 5: Commit** (after Task 3's edits are in)

```bash
git add chromesweep/config.go chromesweep/config_test.go
git commit -m "feat(chromesweep): add Browser presets and reshape Config for multi-browser sweeps"
```

---

### Task 3: Browser-tagged results, two-section report, and the `Run` browser loop

**Files:**
- Modify: `chromesweep/report.go`, `chromesweep/sweep.go`
- Modify test: `chromesweep/report_test.go`
- Modify test: `chromesweep/retarget_test.go`
- Test: `chromesweep/sweep_test.go` (new)

**Interfaces:**
- Consumes: `Browser`, `Config` (Task 2).
- Produces:
  - `type Result struct { Browser, Tag string; Status Status; BrowserVersion, ReportedUA, FramePath, Detail string }`
  - `func localFrameName(browser, tag string) string`
  - `func RenderMarkdown(rep Report) string` (per-browser sections)
  - `func Run(ctx context.Context, c *Cluster, cfg Config, framesDir string) (Report, error)` (unchanged signature)
  - `func sweepOne(ctx context.Context, c *Cluster, cfg Config, b Browser, base baseManifests, tag, framesDir string) Result`
  - `func loadBaseManifests(b Browser, namespace string) (baseManifests, error)`

- [ ] **Step 1: Write the failing tests**

`chromesweep/sweep_test.go`:

```go
package chromesweep

import "testing"

func TestLocalFrameName(t *testing.T) {
	if got := localFrameName("chrome", "130"); got != "chrome-130.png" {
		t.Fatalf("chrome: %q", got)
	}
	// Same tag, different browser must not collide.
	if got := localFrameName("firefox", "130"); got != "firefox-130.png" {
		t.Fatalf("firefox: %q", got)
	}
}
```

Replace `chromesweep/report_test.go`'s `TestRenderMarkdown` with a two-section version and rename `ChromeVersion`→`BrowserVersion`:

```go
func TestRenderMarkdown(t *testing.T) {
	md := RenderMarkdown(Report{
		AnubisImage: "reg/backend:v9",
		Results: []Result{
			{Browser: "chrome", Tag: "150", Status: StatusPass, BrowserVersion: "150.0.7871.114", ReportedUA: "Chrome/150", FramePath: "var/sweep/chrome-150.png"},
			{Browser: "chrome", Tag: "110", Status: StatusFail, Detail: "job failed"},
			{Browser: "firefox", Tag: "152", Status: StatusPass, BrowserVersion: "152.0.5", FramePath: "var/sweep/firefox-152.png"},
		},
	})
	for _, want := range []string{
		"# Chrome version sweep — 1/2 passed",
		"# Firefox version sweep — 1/1 passed",
		"| 150 |", "| 110 |", "job failed", "| 152 |",
		"Anubis image:", "reg/backend:v9",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
	// Chrome section precedes Firefox (first-seen order).
	if strings.Index(md, "Chrome version sweep") > strings.Index(md, "Firefox version sweep") {
		t.Fatalf("browser sections out of order:\n%s", md)
	}
}
```

Also change `TestRenderMarkdownOmitsAnubisWhenEmpty` and `TestRenderJSON`'s `Result` literals to set `Browser: "chrome"` (and use `BrowserVersion` if referenced). Add to `retarget_test.go` a Firefox-host case in the existing `retargetJob` table (or a new test):

```go
func TestRetargetJobFirefox(t *testing.T) {
	job := &batchv1.Job{}
	job.Spec.Template.Spec.Containers = []corev1.Container{{
		Args: []string{"-cdp-url=http://firefox:9222", "-header=Host: localhost:9222"},
	}}
	retargetJob(job, "firefox-smoke-152", "firefox", "firefox-152")
	got := job.Spec.Template.Spec.Containers[0].Args
	if got[0] != "-cdp-url=http://firefox-152:9222" {
		t.Fatalf("cdp url not rewritten: %q", got[0])
	}
	if got[1] != "-header=Host: localhost:9222" {
		t.Fatalf("localhost host must be untouched: %q", got[1])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./chromesweep/ -run 'TestLocalFrameName|TestRenderMarkdown|TestRetargetJobFirefox'`
Expected: build/compile failure (`localFrameName`, `BrowserVersion` undefined).

- [ ] **Step 3: Update `chromesweep/report.go`**

Replace `Result` and `RenderMarkdown`:

```go
// Result is the outcome of testing one browser image tag.
type Result struct {
	Browser        string `json:"browser,omitempty"`
	Tag            string `json:"tag"`
	Status         Status `json:"status"`
	BrowserVersion string `json:"browser_version,omitempty"`
	ReportedUA     string `json:"reported_ua,omitempty"`
	FramePath      string `json:"frame_path,omitempty"`
	Detail         string `json:"detail,omitempty"`
}
```

```go
// RenderMarkdown produces a human-readable summary: one section per browser (in
// first-seen order), each with its own pass count and results table.
func RenderMarkdown(rep Report) string {
	var b strings.Builder
	if rep.AnubisImage != "" {
		fmt.Fprintf(&b, "Anubis image: `%s`\n\n", rep.AnubisImage)
	}
	var order []string
	groups := map[string][]Result{}
	for _, r := range rep.Results {
		if _, ok := groups[r.Browser]; !ok {
			order = append(order, r.Browser)
		}
		groups[r.Browser] = append(groups[r.Browser], r)
	}
	for _, br := range order {
		rs := groups[br]
		passed := 0
		for _, r := range rs {
			if r.Status == StatusPass {
				passed++
			}
		}
		fmt.Fprintf(&b, "# %s version sweep — %d/%d passed\n\n", titleCase(br), passed, len(rs))
		b.WriteString("| tag | status | browser version | frame | detail |\n")
		b.WriteString("|-----|--------|-----------------|-------|--------|\n")
		for _, r := range rs {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
				r.Tag, r.Status, dash(r.BrowserVersion), dash(r.FramePath), dash(r.Detail))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// titleCase upper-cases the first byte of s ("chrome" -> "Chrome").
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
```

- [ ] **Step 4: Rewrite `chromesweep/sweep.go`**

Change `loadBaseManifests`, `Run`, and `sweepOne` to be browser-driven, and add `localFrameName`. Key edits:

```go
func loadBaseManifests(b Browser, namespace string) (baseManifests, error) {
	dep, err := loadDeployment(b.DeploymentManifest, namespace)
	if err != nil {
		return baseManifests{}, err
	}
	svc, err := loadService(b.ServiceManifest, namespace)
	if err != nil {
		return baseManifests{}, err
	}
	np, err := loadNetworkPolicy(b.NetworkPolicyManifest, namespace)
	if err != nil {
		return baseManifests{}, err
	}
	job, err := loadJob(b.JobManifest, namespace)
	if err != nil {
		return baseManifests{}, err
	}
	return baseManifests{deployment: dep, service: svc, netpol: np, job: job}, nil
}

// Run sweeps every browser in cfg.Browsers against one in-cluster Anubis setup:
// Anubis is re-imaged (if overridden) and the frame collector is created ONCE,
// then each browser's versions are tested in a bounded pool of cfg.Parallelism.
// Captured frames are copied into framesDir. A failure on one version is recorded
// and does not stop the others. Results are returned browser-then-version ordered.
func Run(ctx context.Context, c *Cluster, cfg Config, framesDir string) (Report, error) {
	anubisImage, restoreAnubis, err := prepareAnubis(ctx, c, cfg)
	if err != nil {
		return Report{}, err
	}
	defer restoreAnubis()

	if err := c.EnsureCollector(ctx, collectorPodName, cfg.CollectorPVC, cfg.ReadyTimeout); err != nil {
		return Report{}, err
	}
	defer func() {
		if derr := c.DeleteCollector(context.Background(), collectorPodName); derr != nil {
			slog.Warn("collector cleanup failed", "err", derr)
		}
	}()

	parallelism := cfg.Parallelism
	if parallelism < 1 {
		parallelism = 1
	}

	var results []Result
	for _, b := range cfg.Browsers {
		base, err := loadBaseManifests(b, cfg.Namespace)
		if err != nil {
			return Report{}, err
		}
		brResults := make([]Result, len(b.Versions))
		sem := make(chan struct{}, parallelism)
		var wg sync.WaitGroup
		for i, tag := range b.Versions {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, tag string) {
				defer wg.Done()
				defer func() { <-sem }()
				brResults[i] = sweepOne(ctx, c, cfg, b, base, tag, framesDir)
			}(i, tag)
		}
		wg.Wait()
		results = append(results, brResults...)
	}
	return Report{AnubisImage: anubisImage, Results: results}, nil
}
```

`sweepOne` — take `b Browser`, read target fields off it, tag the result, namespace the frame:

```go
func sweepOne(ctx context.Context, c *Cluster, cfg Config, b Browser, base baseManifests, tag, framesDir string) Result {
	name := versionedName(b.Deployment, tag) // e.g. chrome-150 / firefox-152
	jobName := versionedName(b.JobName, tag)  // e.g. chrome-smoke-150
	image := fmt.Sprintf("%s:%s", b.ImageRepo, tag)
	res := Result{Browser: b.Name, Tag: tag}
	log := slog.With("browser", b.Name, "tag", tag, "image", image, "name", name)
	log.Info("testing version")

	defer func() {
		if derr := c.DeleteVersionResources(context.Background(), name, jobName); derr != nil {
			log.Warn("teardown failed", "err", derr)
		}
	}()

	dep := base.deployment.DeepCopy()
	retargetDeployment(dep, name, b.Container, image)
	if err := c.CreateOrReplaceDeployment(ctx, dep, cfg.ReadyTimeout); err != nil {
		res.Status, res.Detail = StatusError, err.Error()
		return res
	}
	svc := base.service.DeepCopy()
	retargetService(svc, name)
	if err := c.CreateOrReplaceService(ctx, svc, cfg.ReadyTimeout); err != nil {
		res.Status, res.Detail = StatusError, err.Error()
		return res
	}
	np := base.netpol.DeepCopy()
	retargetNetworkPolicy(np, name)
	if err := c.CreateOrReplaceNetworkPolicy(ctx, np, cfg.ReadyTimeout); err != nil {
		res.Status, res.Detail = StatusError, err.Error()
		return res
	}
	if err := c.WaitDeploymentReady(ctx, name, cfg.ReadyTimeout); err != nil {
		res.Status, res.Detail = StatusNotReady, err.Error()
		return res
	}

	job := base.job.DeepCopy()
	retargetJob(job, jobName, b.Deployment, name)
	if err := c.ReplaceJob(ctx, job); err != nil {
		res.Status, res.Detail = StatusError, err.Error()
		return res
	}
	ok, err := c.WaitJob(ctx, jobName, cfg.JobTimeout)
	if err != nil {
		res.Status, res.Detail = StatusError, err.Error()
		return res
	}

	if smokeLogs, lerr := c.JobContainerLogs(ctx, jobName, "smoke"); lerr == nil {
		res.ReportedUA = reportedUA(smokeLogs)
	} else {
		log.Warn("reading smoke logs failed", "err", lerr)
	}
	if bullyLogs, lerr := c.JobContainerLogs(ctx, jobName, "chrome-bully"); lerr == nil {
		if remote, perr := capturedFramePath(bullyLogs); perr == nil {
			res.BrowserVersion = versionFromFrame(remote)
			local := filepath.Join(framesDir, localFrameName(b.Name, tag))
			if cerr := c.CopyFrame(ctx, collectorPodName, remote, local); cerr == nil {
				res.FramePath = local
			} else {
				log.Warn("frame copy failed", "err", cerr)
			}
		}
	} else {
		log.Warn("reading chrome-bully logs failed", "err", lerr)
	}

	if ok {
		res.Status = StatusPass
	} else {
		res.Status, res.Detail = StatusFail, "smoke job failed"
	}
	return res
}

// localFrameName is the on-disk name for a captured frame, namespaced by browser
// so same-numbered tags across browsers (chrome 130 and firefox 130) don't collide.
func localFrameName(browser, tag string) string {
	return browser + "-" + tag + ".png"
}
```

Leave `versionFromFrame` as-is (it parses the remote `chrome-<ver>-<ts>.png` name `chrome-bully` writes for both browsers).

- [ ] **Step 5: Run the full package test suite**

Run: `go test ./chromesweep/`
Expected: PASS. Then `go vet ./chromesweep/` — no errors.

- [ ] **Step 6: Commit**

```bash
git add chromesweep/report.go chromesweep/report_test.go chromesweep/sweep.go chromesweep/sweep_test.go chromesweep/retarget_test.go
git commit -m "feat(chromesweep): tag results by browser, per-browser report sections, browser loop in Run"
```

---

### Task 4: Proto — `browser_version` + `browser` fields; regenerate

**Files:**
- Modify: `pb/techaro/lol/gubal/v1/gubal.proto`
- Regenerate: `gen/techaro/lol/gubal/v1/*`

**Interfaces:**
- Consumes: nothing.
- Produces: `ChromeVersionResult` with `GetBrowser() string` (field 6), `GetBrowserVersion() string` (field 3, renamed from `chrome_version`). `SmokeTestRequest` validation unchanged.

- [ ] **Step 1: Edit the result message in `pb/techaro/lol/gubal/v1/gubal.proto`**

Change the `ChromeVersionResult` message; leave `SmokeTestRequest` (and its `min_items` rules) untouched:

```proto
// ChromeVersionResult mirrors chromesweep.Result: the outcome of testing one
// browser image tag. The captured screenshot is not carried here — frames are
// uploaded to object storage separately.
message ChromeVersionResult {
  string tag = 1;
  SweepStatus status = 2;
  string browser_version = 3;
  string reported_ua = 4;
  string detail = 5;
  string browser = 6; // "chrome" / "firefox"
}
```

- [ ] **Step 2: Lint and regenerate**

Run: `buf lint && buf generate`
Expected: no lint errors; `gen/techaro/lol/gubal/v1/gubal.pb.go` now defines `ChromeVersionResult.Browser` and `ChromeVersionResult.BrowserVersion` with `GetBrowser()`/`GetBrowserVersion()`.

- [ ] **Step 3: Verify the generated getters exist**

Run: `grep -E 'func \(x \*ChromeVersionResult\) Get(Browser|BrowserVersion)\(\)' gen/techaro/lol/gubal/v1/gubal.pb.go`
Expected: both `GetBrowser()` and `GetBrowserVersion()` print.

- [ ] **Step 4: Commit**

```bash
git add pb/techaro/lol/gubal/v1/gubal.proto gen/
git commit -m "feat(proto): browser + browser_version on result; regenerate"
```

Note: `cmd/gubald` won't build until Task 5 (it still assigns `ChromeVersion:`/reads `r.ChromeVersion`). Task 4's gate is `buf lint` + the grep, not a full build.

---

### Task 5: gubald wiring — assemble both browsers, map results

**Files:**
- Modify: `cmd/gubald/svc/smoketest/smoketest.go`
- Modify test: `cmd/gubald/svc/smoketest/smoketest_test.go`

**Interfaces:**
- Consumes: `chromesweep.ChromeBrowser`/`FirefoxBrowser`/`Browser`/`Config`/`Run`/`Result` (Tasks 2–3); `gubalv1.ChromeVersionResult` with `Browser`/`BrowserVersion` (Task 4).
- Produces: `func browsersFor(req *gubalv1.SmokeTestRequest) ([]chromesweep.Browser, error)`.

- [ ] **Step 1: Write the failing tests**

Fix the `valid` case and add Firefox cases in `smoketest_test.go` (the `valid` case must now carry both version lists), and add a `browsersFor` test:

```go
// in the TestSmokeTestRequestValidation table, replace the "valid" case with:
{
	name: "valid",
	req:  &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "ghcr.io/techarohq/anubis:latest", ChromeVersions: []int32{120, 150}, FirefoxVersions: []int32{140, 152}},
},
// and add:
{
	name:    "missing firefox versions",
	req:     &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "x", ChromeVersions: []int32{120}},
	wantErr: true,
},
{
	name:    "firefox version too low",
	req:     &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "x", ChromeVersions: []int32{120}, FirefoxVersions: []int32{100}},
	wantErr: true,
},
```

```go
func TestBrowsersFor(t *testing.T) {
	req := &gubalv1.SmokeTestRequest{ChromeVersions: []int32{120, 150}, FirefoxVersions: []int32{140, 152}}
	bs, err := browsersFor(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 2 || bs[0].Name != "chrome" || bs[1].Name != "firefox" {
		t.Fatalf("browsers = %+v", bs)
	}
	if strings.Join(bs[0].Versions, ",") != "120,150" {
		t.Fatalf("chrome versions = %v", bs[0].Versions)
	}
	if strings.Join(bs[1].Versions, ",") != "140,152" {
		t.Fatalf("firefox versions = %v", bs[1].Versions)
	}
}
```

(Add `"strings"` to the test imports.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/gubald/svc/smoketest/ -run 'TestBrowsersFor'`
Expected: build failure — `browsersFor` undefined.

- [ ] **Step 3: Implement `browsersFor` and rewire the handler in `smoketest.go`**

Add the helper and replace the version-parsing + config-building + result-mapping:

```go
// browsersFor builds the browser targets for a request: Chrome + Firefox presets
// carrying the requested versions. Validation guarantees both lists are non-empty,
// but a browser with no versions is simply omitted so this degrades safely.
func browsersFor(req *gubalv1.SmokeTestRequest) ([]chromesweep.Browser, error) {
	var browsers []chromesweep.Browser
	if len(req.GetChromeVersions()) > 0 {
		vs, err := parseInts(req.GetChromeVersions())
		if err != nil {
			return nil, err
		}
		b := chromesweep.ChromeBrowser()
		b.Versions = vs
		browsers = append(browsers, b)
	}
	if len(req.GetFirefoxVersions()) > 0 {
		vs, err := parseInts(req.GetFirefoxVersions())
		if err != nil {
			return nil, err
		}
		b := chromesweep.FirefoxBrowser()
		b.Versions = vs
		browsers = append(browsers, b)
	}
	if len(browsers) == 0 {
		return nil, fmt.Errorf("no versions given")
	}
	return browsers, nil
}

// parseInts renders int32 majors as strings and runs them through ParseVersions
// (trim/dedupe/non-empty).
func parseInts(vs []int32) ([]string, error) {
	raw := make([]string, len(vs))
	for i, v := range vs {
		raw[i] = strconv.Itoa(int(v))
	}
	return chromesweep.ParseVersions(raw)
}
```

In `SmokeTest`, replace the `raw`/`versions`/`cfg.Versions` block with:

```go
	browsers, err := browsersFor(req)
	if err != nil {
		return nil, twirp.NewError(twirp.InvalidArgument, err.Error())
	}

	cs, err := loadClientset()
	if err != nil {
		return nil, twirp.InternalErrorWith(fmt.Errorf("building kube client: %w", err))
	}

	cfg := chromesweep.DefaultConfig()
	cfg.AnubisImage = req.GetAnubisImage()
	cfg.Browsers = browsers
```

And the result mapping:

```go
	for _, r := range rep.Results {
		result.Results = append(result.Results, &gubalv1.ChromeVersionResult{
			Browser:        r.Browser,
			Tag:            r.Tag,
			Status:         sweepStatus(r.Status),
			BrowserVersion: r.BrowserVersion,
			ReportedUa:     r.ReportedUA,
			Detail:         r.Detail,
		})
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/gubald/...`
Expected: PASS (validation table incl. new Firefox cases, and `TestBrowsersFor`).

- [ ] **Step 5: Commit**

```bash
git add cmd/gubald/svc/smoketest/smoketest.go cmd/gubald/svc/smoketest/smoketest_test.go
git commit -m "feat(gubald): sweep Chrome and Firefox from one request"
```

---

### Task 6: chrome-sweep CLI — Chrome + Firefox version flags

**Files:**
- Modify: `cmd/chrome-sweep/main.go`

**Interfaces:**
- Consumes: `chromesweep.DefaultConfig`/`ChromeBrowser`/`FirefoxBrowser`/`ParseVersions`/`Run` (Tasks 2–3).
- Produces: a CLI whose `cfg.Browsers` is built from `-chrome-versions`/`-firefox-versions` (each defaulting to the preset list).

- [ ] **Step 1: Replace flag wiring in `main()`**

Remove the per-browser override flags (`-deployment`, `-container`, `-image-repo`, `-job-manifest`, `-job-name`, `-deployment-manifest`, `-service-manifest`, `-networkpolicy-manifest`) and the positional-versions parsing. Keep the run-wide flags. Add comma-list version flags defaulting to the preset lists:

```go
	cfg := chromesweep.DefaultConfig()
	kubeconfig := flag.String("kubeconfig", defaultKubeconfig(), "path to kubeconfig")
	flag.StringVar(&cfg.Namespace, "namespace", cfg.Namespace, "namespace holding the browser resources and smoke Jobs")
	flag.StringVar(&cfg.AnubisManifest, "anubis-manifest", cfg.AnubisManifest, "Anubis Deployment manifest; the tested Anubis image ref is read from it")
	flag.StringVar(&cfg.AnubisContainer, "anubis-container", cfg.AnubisContainer, "container in the Anubis Deployment that holds the Anubis image")
	flag.StringVar(&cfg.AnubisImage, "anubis-image", cfg.AnubisImage, "override the Anubis image ref (default: the ref from -anubis-manifest); when set, the live Anubis Deployment is re-imaged for the run and restored after")
	flag.IntVar(&cfg.Parallelism, "parallelism", cfg.Parallelism, "max number of versions tested concurrently")
	flag.StringVar(&cfg.CollectorPVC, "pvc", cfg.CollectorPVC, "PVC that holds captured frames")
	flag.StringVar(&cfg.OutDir, "out", cfg.OutDir, "directory to write the report and copied frames into")
	flag.DurationVar(&cfg.ReadyTimeout, "ready-timeout", cfg.ReadyTimeout, "max wait for a version's rollout to become Ready")
	flag.DurationVar(&cfg.JobTimeout, "job-timeout", cfg.JobTimeout, "max wait for a smoke Job to finish")

	// Default to the preset version lists; either flag overrides its browser.
	chromeVersions := flag.String("chrome-versions", strings.Join(chromesweep.ChromeBrowser().Versions, ","), "comma-separated Chrome major versions to sweep (empty to skip Chrome)")
	firefoxVersions := flag.String("firefox-versions", strings.Join(chromesweep.FirefoxBrowser().Versions, ","), "comma-separated Firefox major versions to sweep (empty to skip Firefox)")
	flag.Parse()

	browsers, err := browsersFromFlags(*chromeVersions, *firefoxVersions)
	if err != nil {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
		slog.Error("bad versions", "err", err)
		os.Exit(1)
	}
	cfg.Browsers = browsers
```

- [ ] **Step 2: Add `browsersFromFlags` helper**

```go
// browsersFromFlags builds the browser targets from comma-separated version
// lists. An empty list skips that browser; at least one browser must survive.
func browsersFromFlags(chromeCSV, firefoxCSV string) ([]chromesweep.Browser, error) {
	var browsers []chromesweep.Browser
	for _, spec := range []struct {
		csv     string
		browser chromesweep.Browser
	}{
		{chromeCSV, chromesweep.ChromeBrowser()},
		{firefoxCSV, chromesweep.FirefoxBrowser()},
	} {
		fields := strings.Split(spec.csv, ",")
		vs, err := chromesweep.ParseVersions(fields)
		if err != nil {
			// Empty list -> skip this browser rather than failing the run.
			if strings.TrimSpace(spec.csv) == "" {
				continue
			}
			return nil, fmt.Errorf("%s: %w", spec.browser.Name, err)
		}
		b := spec.browser
		b.Versions = vs
		browsers = append(browsers, b)
	}
	if len(browsers) == 0 {
		return nil, fmt.Errorf("no versions given for any browser")
	}
	return browsers, nil
}
```

Add `"strings"` to imports; remove now-unused ones. Update the package doc comment to say Chrome and Firefox.

- [ ] **Step 3: Build the CLI**

Run: `go build -o ./var/chrome-sweep ./cmd/chrome-sweep`
Expected: builds clean (into `./var`). Then `go vet ./cmd/chrome-sweep`.

- [ ] **Step 4: Smoke-check flag defaults**

Run: `./var/chrome-sweep -h 2>&1 | grep -E 'chrome-versions|firefox-versions'`
Expected: both flags listed with their preset default lists shown.

- [ ] **Step 5: Commit**

```bash
git add cmd/chrome-sweep/main.go
git commit -m "feat(chrome-sweep): drive Chrome and Firefox via -chrome-versions/-firefox-versions"
```

---

### Task 7: gubalctl — Firefox versions flag; final full-suite gate

**Files:**
- Modify: `cmd/gubalctl/main.go`

**Interfaces:**
- Consumes: `gubalv1.SmokeTestRequest.FirefoxVersions` (Task 4).
- Produces: a client that sends both version lists.

- [ ] **Step 1: Add the flag and default the Chrome flag**

Update the flag vars:

```go
	chromeVersions  = flag.String("chrome-versions", "75,80,85,90,95,100,105,110,115,120,125,130,135,140,145,150", "comma-separated Chrome major versions to test")
	firefoxVersions = flag.String("firefox-versions", "129,135,140,145,150,152", "comma-separated Firefox major versions to test")
```

- [ ] **Step 2: Parse and send both**

Replace the single-parse + request build:

```go
	chromeVs, err := parseVersions(*chromeVersions, "chrome")
	if err != nil {
		return err
	}
	firefoxVs, err := parseVersions(*firefoxVersions, "firefox")
	if err != nil {
		return err
	}
```

```go
	slog.InfoContext(ctx, "submitting smoke test", "url", *baseURL, "id", reqID, "anubis_image", *anubisImage, "chrome_versions", chromeVs, "firefox_versions", firefoxVs)
	res, err := client.SmokeTest(ctx, &gubalv1.SmokeTestRequest{
		Id:              reqID,
		AnubisImage:     *anubisImage,
		ChromeVersions:  chromeVs,
		FirefoxVersions: firefoxVs,
	})
```

Generalize `parseVersions` to take the browser name for its error messages:

```go
// parseVersions turns a comma-separated list of majors into int32s.
func parseVersions(s, browser string) ([]int32, error) {
	fields := strings.Split(s, ",")
	out := make([]int32, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("invalid %s version %q: %w", browser, f, err)
		}
		out = append(out, int32(n))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-%s-versions is required (comma-separated)", browser)
	}
	return out, nil
}
```

- [ ] **Step 3: Build gubalctl**

Run: `go build -o ./var/gubalctl ./cmd/gubalctl`
Expected: builds clean into `./var`.

- [ ] **Step 4: Full-suite gate**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all PASS. In particular `./cmd/gubald/svc/smoketest/` (previously red) now passes.

- [ ] **Step 5: Commit**

```bash
git add cmd/gubalctl/main.go
git commit -m "feat(gubalctl): send Firefox versions alongside Chrome"
```

---

### Task 8: Real-cluster verification (manual)

The `Run` browser loop, live rollout, `kubectl cp` frame copy, and the Firefox image's CDP behavior are only exercised against a real cluster. This task is manual and gated on cluster access + a pushed `firefox:<tag>` image.

- [ ] **Step 1: Confirm prerequisites in `ci`** — Anubis Deployment + `anubis-key` secret + `chrome-bully-data` PVC exist; `ghcr.io/techarohq/gubal/firefox:152` (and other swept tags) are pushed.

- [ ] **Step 2: Run a small Firefox-only sweep from the repo root**

```bash
go build -o ./var/chrome-sweep ./cmd/chrome-sweep
./var/chrome-sweep -chrome-versions "" -firefox-versions 152 -out ./var/sweep
```

Expected: `report.md` shows a `# Firefox version sweep — 1/1 passed` section with a `browser version` and a copied frame; `./var/sweep/report.zip` contains `frames/firefox-152.png`.

- [ ] **Step 3: Confirm the Firefox `smoke` assertion** — check the `firefox-smoke-152` Job's `smoke` container logs show `PASS: driveable Firefox UA`. If the Firefox CDP `/json/version` payload differs from Chrome's (no `User-Agent` field, or a different shape), adjust the `smoke` container's `curl`/`sed` in `k8s/firefox/smoke-job.yaml` to match, then re-run.

- [ ] **Step 4: Run a combined sweep** — `./var/chrome-sweep -chrome-versions 150 -firefox-versions 152` and confirm both sections render and frames `chrome-150.png` + `firefox-152.png` are both present (no collision).

- [ ] **Step 5 (optional): Update `k8s/README.md`** if the Firefox CDP quirks differ from Chrome's, documenting them alongside the existing CDP notes. Commit any manifest/doc fixes with `fix(firefox): ...` / `docs(firefox): ...`.

---

## Self-Review

**Spec coverage:**
- §1 Config split + presets + default versions → Task 2. ✓
- §2 shared setup + browser loop → Task 3. ✓
- §3 Result.Browser, BrowserVersion, two-section report, frame collision → Task 3. ✓
- §4 Firefox manifests + UA/label specifics → Task 1; CDP `/json/version` assumption → Task 8. ✓
- §5 proto (strict validation kept) → Task 4; gubald → Task 5; gubalctl → Task 7; CLI → Task 6. ✓
- §6 tests → Tasks 2/3/5 (presets, report grouping, localFrameName, retargetJob firefox, browsersFor, validation). ✓

**Placeholder scan:** No TBD/TODO; every code step shows full code; test steps show assertions. Task 8 is explicitly manual (real-cluster), not a placeholder.

**Type consistency:** `Browser` fields, `Config.Browsers`, `Result.Browser`/`BrowserVersion`, `localFrameName(browser, tag)`, `browsersFor(req)`, `browsersFromFlags(chromeCSV, firefoxCSV)`, `parseInts`, `parseVersions(s, browser)`, proto `GetBrowser()`/`GetBrowserVersion()` — names match across tasks. `loadJob` is assumed to exist (used by `loadBaseManifests` today); `EnsureCollector`/`DeleteCollector`/`CreateOrReplace*`/`DeleteVersionResources`/`WaitJob`/`WaitDeploymentReady`/`JobContainerLogs`/`CopyFrame` are unchanged existing `Cluster` methods.

**Note on task independence:** Tasks 2 and 3 together reshape the `chromesweep` package; the package does not compile between them. Execute them as one review unit (Task 2's green gate runs after Task 3's edits land). Tasks 4→5 similarly pair proto regen with the gubald build; Task 4's gate is `buf lint` + grep, and the full build goes green at Task 5. The entire tree is green at Task 7 Step 4.
