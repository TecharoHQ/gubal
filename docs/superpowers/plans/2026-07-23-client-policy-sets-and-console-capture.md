# Client Policy Sets, Console Capture, and Bundle Layout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move Anubis rulesets out of the `chromesweep` `go:embed` into `test/gubal/`, let clients submit them as a `map<string,string>` on `SmokeTestRequest`, forward chrome-bully's devtools console to slog, and give report bundles per-policy subfolders that the results table links to.

**Architecture:** `chromesweep` stops compiling policies in and gains two loaders — `LoadPoliciesFromDir` (disk, for the `chrome-sweep` CLI) and `PoliciesFromMap` (wire, for `gubald`). The proto gains a required `policies` map so gubald needs no policy files on disk. Two new `Result` methods become the single source of truth for bundle layout, used by both `WriteBundle` and `RenderMarkdown`. chrome-bully attaches a CDP event listener that forwards console output to slog, which already flows into the bundled container log.

**Tech Stack:** Go 1.26.4, client-go, chromedp/cdproto, Twirp + protovalidate (via `buf`), Kubernetes.

## Global Constraints

- Go module `github.com/TecharoHQ/gubal`, Go 1.26.4.
- **Build binaries into `./var`, never the repo root:** `go build -o ./var/<name> ./cmd/<name>`.
- Use `log/slog` (JSON to stderr), never the stdlib `log`.
- All CLIs use flagenv: `flagenv.Parse()` then `flag.Parse()`. Kebab-case flags map to `UPPER_SNAKE_CASE` env vars (`-policy-dir` → `POLICY_DIR`).
- protovalidate rules compile at first validation (runtime), not build time — a malformed rule surfaces only as a Twirp `invalid_argument: compilation error`, so every new rule must be covered by a unit test.
- On a `repeated` field, per-element rules go under `repeated.items`. On a `map` field, they go under `map.keys` / `map.values`.
- Do not bump `buf.build/gen/go/bufbuild/protovalidate/...`; its version must stay aligned with the one `within.website/x` uses.
- Policy names become Kubernetes ConfigMap names (`anubis-policy-<name>`), so they must be DNS-safe: lowercase letters, digits, `-`.
- Run `go test ./...` and `go vet ./...` before each commit.

---

### Task 1: Move policies to `test/gubal/` and replace the embed with loaders

**Files:**
- Move: `chromesweep/policies/*.yaml` → `test/gubal/*.yaml`
- Move: `chromesweep/policies/README.md` → `test/gubal/README.md`
- Modify: `chromesweep/policies.go` (full rewrite)
- Modify: `chromesweep/config.go:9-12` (imports), `:74-76` (comment), `:93` (drop `Policies`), `:97-106` (delete `mustLoadPolicies`)
- Test: `chromesweep/policies_test.go` (full rewrite)
- Modify: `chromesweep/config_test.go:48-60` (delete `TestDefaultConfigLoadsPolicies`)

**Interfaces:**
- Consumes: nothing (first task).
- Produces:
  - `func LoadPoliciesFromDir(dir string) ([]Policy, error)` — reads `*.yaml`, sorted by `Name`, errors on missing/empty dir.
  - `func PoliciesFromMap(m map[string]string) []Policy` — name→YAML, sorted by name, `nil` for an empty map.
  - `type Policy struct { Name string; Content []byte }` — unchanged.
  - `DefaultConfig().Policies` is now `nil`; every caller must fill it.

- [ ] **Step 1: Move the policy files with git**

```bash
mkdir -p test/gubal
git mv chromesweep/policies/default-config.yaml test/gubal/default-config.yaml
git mv chromesweep/policies/fast.yaml test/gubal/fast.yaml
git mv chromesweep/policies/metarefresh.yaml test/gubal/metarefresh.yaml
git mv chromesweep/policies/preact.yaml test/gubal/preact.yaml
git mv chromesweep/policies/README.md test/gubal/README.md
rmdir chromesweep/policies
```

- [ ] **Step 2: Write the failing tests**

Replace the entire contents of `chromesweep/policies_test.go`:

```go
package chromesweep

import (
	"os"
	"path/filepath"
	"testing"
)

// writePolicyDir builds a temp dir holding the given filename -> content pairs.
func writePolicyDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadPoliciesFromDir(t *testing.T) {
	dir := writePolicyDir(t, map[string]string{
		"fast.yaml":           "bots: [fast]",
		"default-config.yaml": "bots: [default]",
		"notes.txt":           "not a policy",
	})

	got, err := LoadPoliciesFromDir(dir)
	if err != nil {
		t.Fatalf("LoadPoliciesFromDir: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 policies (non-yaml skipped), got %d: %+v", len(got), got)
	}
	// Sorted ascending, extension stripped.
	if got[0].Name != "default-config" || got[1].Name != "fast" {
		t.Fatalf("names = %q, %q; want default-config, fast", got[0].Name, got[1].Name)
	}
	if string(got[0].Content) != "bots: [default]" {
		t.Fatalf("default-config content = %q", got[0].Content)
	}
}

func TestLoadPoliciesFromDirErrors(t *testing.T) {
	if _, err := LoadPoliciesFromDir(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("a missing directory must error")
	}
	dir := writePolicyDir(t, map[string]string{"README.md": "no rulesets here"})
	if _, err := LoadPoliciesFromDir(dir); err == nil {
		t.Fatal("a directory with no *.yaml must error")
	}
}

func TestPoliciesFromMap(t *testing.T) {
	got := PoliciesFromMap(map[string]string{
		"fast":           "bots: [fast]",
		"default-config": "bots: [default]",
		"preact":         "bots: [preact]",
	})
	if len(got) != 3 {
		t.Fatalf("want 3 policies, got %d", len(got))
	}
	// Sorted, so pass ordering never depends on map iteration order.
	for i, want := range []string{"default-config", "fast", "preact"} {
		if got[i].Name != want {
			t.Fatalf("policy %d = %q, want %q", i, got[i].Name, want)
		}
	}
	if string(got[1].Content) != "bots: [fast]" {
		t.Fatalf("fast content = %q", got[1].Content)
	}
	if PoliciesFromMap(nil) != nil {
		t.Fatal("an empty map must yield nil")
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./chromesweep/ -run 'TestLoadPoliciesFromDir|TestPoliciesFromMap' -v`
Expected: FAIL — build error, `undefined: LoadPoliciesFromDir` and `undefined: PoliciesFromMap`. (The old `LoadPolicies` still exists and its embed now matches no files, which is itself a build error.)

- [ ] **Step 4: Rewrite `chromesweep/policies.go`**

Replace the entire contents:

```go
package chromesweep

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Policy is one Anubis botPolicies ruleset. Name is the file's base name without
// the .yaml extension (e.g. "default-config"); it names the test pass in reports
// and the per-policy ConfigMap that carries the ruleset into the Anubis pod.
type Policy struct {
	Name    string
	Content []byte
}

// LoadPoliciesFromDir reads every *.yaml in dir and returns the rulesets sorted
// by Name, so a sweep's pass ordering is stable. Non-YAML files and
// subdirectories are ignored.
//
// A missing directory, or one holding no rulesets, is an error: since policies
// are no longer compiled into the binary, an empty set means a misconfigured run
// rather than a deliberate one.
func LoadPoliciesFromDir(dir string) ([]Policy, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading policy dir %s: %w", dir, err)
	}
	var policies []Policy
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading policy %s: %w", e.Name(), err)
		}
		policies = append(policies, Policy{
			Name:    strings.TrimSuffix(e.Name(), ".yaml"),
			Content: content,
		})
	}
	if len(policies) == 0 {
		return nil, fmt.Errorf("no *.yaml policies in %s", dir)
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].Name < policies[j].Name })
	return policies, nil
}

// PoliciesFromMap converts a wire map of policy name -> ruleset YAML into
// policies sorted by name, so a sweep's pass ordering does not depend on Go's
// randomized map iteration. An empty map yields nil, which chromesweep.Run
// treats as "sweep once against Anubis's live ruleset".
func PoliciesFromMap(m map[string]string) []Policy {
	if len(m) == 0 {
		return nil
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	policies := make([]Policy, 0, len(names))
	for _, name := range names {
		policies = append(policies, Policy{Name: name, Content: []byte(m[name])})
	}
	return policies
}
```

- [ ] **Step 5: Drop the embedded policies from `DefaultConfig`**

In `chromesweep/config.go`, change the import block (`fmt` was used only by `mustLoadPolicies`):

```go
import (
	"time"
)
```

Update the `Policies` field comment:

```go
	// Policies are the Anubis rulesets to sweep every browser against, one test
	// pass per policy. Callers fill this in: chrome-sweep loads it from
	// -policy-dir, gubald takes it from the request. Empty sweeps once against
	// whatever ruleset Anubis is already running.
	Policies []Policy
```

Delete the `Policies: mustLoadPolicies(),` line from `DefaultConfig`, so the struct literal ends:

```go
		Browsers:        []Browser{ChromeBrowser(), FirefoxBrowser()},
	}
}
```

Delete the whole `mustLoadPolicies` function (the last 10 lines of the file).

- [ ] **Step 6: Delete the stale default-config test**

In `chromesweep/config_test.go`, delete `TestDefaultConfigLoadsPolicies` entirely (lines 48-60). It asserts `len(cfg.Policies) >= 2`, which is now nil by design.

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./chromesweep/ && go vet ./chromesweep/`
Expected: PASS.

- [ ] **Step 8: Reword the moved README**

Replace the first paragraph of `test/gubal/README.md` (which claims the files are embedded) with:

```markdown
# Anubis sweep policies

Each `*.yaml` here is a full [Anubis](https://github.com/TecharoHQ/anubis)
`botPolicies` ruleset. Every sweep runs the full browser × version matrix against
**each** ruleset as a separate test pass named by the file's base name (e.g.
`fast.yaml` → the `fast` pass).

These files are read from disk, not compiled in. `chrome-sweep` loads them via
`-policy-dir` (default `test/gubal`), and `gubalctl` reads the same directory and
submits the set to `gubald` in the request. To add a pass: drop a new `*.yaml`
file here. No code change and no rebuild is needed.
```

Leave the existing "Constraints" section unchanged — the naming and CHALLENGE-vs-ALLOW rules still apply.

- [ ] **Step 9: Commit**

```bash
git add chromesweep/ test/gubal/
git commit -m "refactor(chromesweep): load policies from disk instead of go:embed

Moves the Anubis rulesets to test/gubal/ and replaces LoadPolicies with
LoadPoliciesFromDir (disk) and PoliciesFromMap (wire). DefaultConfig no
longer carries policies; callers fill them in."
```

---

### Task 2: Give `chrome-sweep` a `-policy-dir` flag

**Files:**
- Modify: `cmd/chrome-sweep/main.go:35-47` (flag + load)

**Interfaces:**
- Consumes: `chromesweep.LoadPoliciesFromDir(dir string) ([]Policy, error)` from Task 1.
- Produces: nothing other tasks depend on.

- [ ] **Step 1: Add the flag and load the policies**

In `cmd/chrome-sweep/main.go`, insert one new line between the existing `firefoxVersions` declaration and the existing `flag.Parse()` call (do not add a second `flag.Parse()`):

```go
	policyDir := flag.String("policy-dir", "test/gubal", "directory of Anubis botPolicies *.yaml rulesets; each becomes one test pass")
```

Then, immediately after the existing `cfg.Browsers = browsers` line, load them (`err` is already declared above by `browsersFromFlags`, and `policies` is new, so `:=` is correct):

```go
	cfg.Browsers = browsers

	policies, err := chromesweep.LoadPoliciesFromDir(*policyDir)
	if err != nil {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
		slog.Error("bad policy dir", "err", err)
		os.Exit(1)
	}
	cfg.Policies = policies
```

- [ ] **Step 2: Verify it builds and the flag is wired**

```bash
go build -o ./var/chrome-sweep ./cmd/chrome-sweep
./var/chrome-sweep -h 2>&1 | grep -A1 policy-dir
```

Expected: the help text shows `-policy-dir string` with default `test/gubal`.

- [ ] **Step 3: Verify a bad policy dir fails loudly**

```bash
./var/chrome-sweep -policy-dir /nonexistent 2>&1 | head -2
```

Expected: a JSON line with `"msg":"bad policy dir"` and a non-zero exit. It must fail here, before touching the cluster.

- [ ] **Step 4: Run the full test suite**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/chrome-sweep/main.go
git commit -m "feat(chrome-sweep): add -policy-dir flag defaulting to test/gubal"
```

---

### Task 3: Add the `policies` map to the proto and wire it into gubald

**Files:**
- Modify: `pb/techaro/lol/gubal/v1/gubal.proto:12-26` (`SmokeTestRequest`)
- Regenerate: `gen/techaro/lol/gubal/v1/*`
- Modify: `cmd/gubald/svc/smoketest/smoketest.go:88-90` (`executeSweep`)
- Test: `cmd/gubald/svc/smoketest/smoketest_test.go` (new validation cases + fixture updates)
- Modify: `cmd/gubald/svc/smoketest/submit_test.go:45` (fixture)

**Interfaces:**
- Consumes: `chromesweep.PoliciesFromMap(m map[string]string) []Policy` from Task 1.
- Produces: `SmokeTestRequest.Policies map[string]string` (getter `req.GetPolicies()`), required and non-empty.

- [ ] **Step 1: Add the field to the proto**

In `pb/techaro/lol/gubal/v1/gubal.proto`, add to `SmokeTestRequest` after `firefox_versions`:

```proto
  // policies maps a ruleset name to a full Anubis botPolicies YAML document.
  // Every browser version is swept once per policy. The name becomes a
  // ConfigMap named anubis-policy-<name>, hence the DNS-1123 label pattern;
  // max_len 49 keeps that name inside Kubernetes' 63-character limit.
  map<string, string> policies = 5 [
    (buf.validate.field).map.min_pairs = 1,
    (buf.validate.field).map.keys.string.pattern = "^[a-z0-9]([a-z0-9-]*[a-z0-9])?$",
    (buf.validate.field).map.keys.string.max_len = 49,
    (buf.validate.field).map.values.string.min_len = 1
  ];
```

- [ ] **Step 2: Regenerate**

```bash
buf lint && buf generate
git status --short gen/
```

Expected: `buf lint` is silent, and `gen/techaro/lol/gubal/v1/gubal.pb.go` shows as modified with a new `Policies` field.

- [ ] **Step 3: Write the failing validation tests**

In `cmd/gubald/svc/smoketest/smoketest_test.go`, add this helper directly above `TestSmokeTestRequestValidation`:

```go
// testPolicies is a minimal valid policy map for request fixtures.
func testPolicies() map[string]string {
	return map[string]string{"default-config": "bots: []"}
}
```

Add `Policies: testPolicies(),` to every request literal already expected to be **valid** — the `"valid"` case in `TestSmokeTestRequestValidation` (line 25) and the `valid` variable in `TestSubmitSmokeTestRequestValidation` (line 94). For example:

```go
		{
			name: "valid",
			req:  &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "ghcr.io/techarohq/anubis:latest", ChromeVersions: []int32{120, 150}, FirefoxVersions: []int32{140, 152}, Policies: testPolicies()},
		},
```

Then add these cases to the `TestSmokeTestRequestValidation` table:

```go
		{
			name:    "missing policies",
			req:     &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "x", ChromeVersions: []int32{120}, FirefoxVersions: []int32{140}},
			wantErr: true,
		},
		{
			name:    "empty policies map",
			req:     &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "x", ChromeVersions: []int32{120}, FirefoxVersions: []int32{140}, Policies: map[string]string{}},
			wantErr: true,
		},
		{
			name:    "policy name not dns-safe",
			req:     &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "x", ChromeVersions: []int32{120}, FirefoxVersions: []int32{140}, Policies: map[string]string{"Default_Config": "bots: []"}},
			wantErr: true,
		},
		{
			name:    "policy name too long for a configmap",
			req:     &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "x", ChromeVersions: []int32{120}, FirefoxVersions: []int32{140}, Policies: map[string]string{strings.Repeat("a", 50): "bots: []"}},
			wantErr: true,
		},
		{
			name:    "empty policy body",
			req:     &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "x", ChromeVersions: []int32{120}, FirefoxVersions: []int32{140}, Policies: map[string]string{"default-config": ""}},
			wantErr: true,
		},
```

`strings` is already imported in this file.

Also add `Policies: testPolicies(),` to the fixture at `cmd/gubald/svc/smoketest/submit_test.go:45`, so it stays valid:

```go
		Test:   &gubalv1.SmokeTestRequest{Id: uuid.NewString(), AnubisImage: "x", ChromeVersions: []int32{120}, FirefoxVersions: []int32{140}, Policies: map[string]string{"default-config": "bots: []"}},
```

- [ ] **Step 4: Run the tests to verify the new rules hold**

Run: `go test ./cmd/gubald/... -run Validation -v`
Expected: PASS. A failure reading `invalid_argument: compilation error` means the rule syntax is wrong — per the Global Constraints, map rules go under `map.keys` / `map.values`, not directly on the field.

- [ ] **Step 5: Use the submitted policies in gubald**

In `cmd/gubald/svc/smoketest/smoketest.go`, in `executeSweep`, add one line after `cfg.Browsers = browsers`:

```go
	cfg := chromesweep.DefaultConfig()
	cfg.AnubisImage = req.GetAnubisImage()
	cfg.Browsers = browsers
	// protovalidate guarantees a non-empty, DNS-safe map at the RPC boundary,
	// so this always yields at least one pass.
	cfg.Policies = chromesweep.PoliciesFromMap(req.GetPolicies())
```

- [ ] **Step 6: Run the full suite**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add pb/ gen/ cmd/gubald/
git commit -m "feat(proto): accept a policy set on SmokeTestRequest

Clients now submit name -> botPolicies YAML; gubald sweeps against exactly
that set. An empty map is rejected, since there is no longer a server-side
default policy set."
```

---

### Task 4: Teach `gubalctl` to read `test/gubal` and submit the map

**Files:**
- Create: `cmd/gubalctl/policies.go`
- Test: `cmd/gubalctl/policies_test.go`
- Modify: `cmd/gubalctl/main.go:26-40` (flag), `:64-71` (load), `:92-118` (attach to both requests)

**Interfaces:**
- Consumes: `SmokeTestRequest.Policies` from Task 3.
- Produces: `func loadPolicyDir(dir string) (map[string]string, error)`.

This deliberately does **not** import `chromesweep`: that package links client-go, and gubalctl is a thin CI client. The duplicated logic is a directory listing and a file read.

- [ ] **Step 1: Write the failing test**

Create `cmd/gubalctl/policies_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPolicyDir(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"default-config.yaml": "bots: [default]",
		"fast.yaml":           "bots: [fast]",
		"README.md":           "not a policy",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := loadPolicyDir(dir)
	if err != nil {
		t.Fatalf("loadPolicyDir: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 policies (non-yaml skipped), got %d: %v", len(got), got)
	}
	// Keyed by base name without extension — the name gubald turns into a ConfigMap.
	if got["default-config"] != "bots: [default]" {
		t.Fatalf("default-config = %q", got["default-config"])
	}
	if got["fast"] != "bots: [fast]" {
		t.Fatalf("fast = %q", got["fast"])
	}
}

func TestLoadPolicyDirErrors(t *testing.T) {
	if _, err := loadPolicyDir(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("a missing directory must error")
	}
	if _, err := loadPolicyDir(t.TempDir()); err == nil {
		t.Fatal("an empty directory must error")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/gubalctl/ -run TestLoadPolicyDir -v`
Expected: FAIL — `undefined: loadPolicyDir`.

- [ ] **Step 3: Implement the reader**

Create `cmd/gubalctl/policies.go`:

```go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// loadPolicyDir reads every *.yaml in dir into the name -> ruleset map gubald
// expects, keyed by the file's base name without its extension. Non-YAML files
// and subdirectories are ignored.
//
// This intentionally does not reuse chromesweep.LoadPoliciesFromDir: importing
// that package would link client-go into what is meant to be a thin CI client.
func loadPolicyDir(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading policy dir %s: %w", dir, err)
	}
	policies := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading policy %s: %w", e.Name(), err)
		}
		policies[strings.TrimSuffix(e.Name(), ".yaml")] = string(content)
	}
	if len(policies) == 0 {
		return nil, fmt.Errorf("no *.yaml policies in %s", dir)
	}
	return policies, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/gubalctl/ -run TestLoadPolicyDir -v`
Expected: PASS.

- [ ] **Step 5: Add the flag and attach the map to both requests**

In `cmd/gubalctl/main.go`, add to the `var` block after the `id` flag:

```go
	policyDir = flag.String("policy-dir", "test/gubal", "directory of Anubis botPolicies *.yaml rulesets to test against (env: POLICY_DIR)")
```

In `run`, load them right after the version parsing (after the `firefoxVs` block):

```go
	policies, err := loadPolicyDir(*policyDir)
	if err != nil {
		return err
	}
```

Add `Policies: policies,` to the async request literal:

```go
			Test: &gubalv1.SmokeTestRequest{
				Id:              reqID,
				AnubisImage:     *anubisImage,
				ChromeVersions:  chromeVs,
				FirefoxVersions: firefoxVs,
				Policies:        policies,
			},
```

And to the sync one:

```go
	res, err := client.SmokeTest(ctx, &gubalv1.SmokeTestRequest{
		Id:              reqID,
		AnubisImage:     *anubisImage,
		ChromeVersions:  chromeVs,
		FirefoxVersions: firefoxVs,
		Policies:        policies,
	})
```

Finally, add the policy names to the sync log line so a run records what it tested:

```go
	slog.InfoContext(ctx, "submitting smoke test", "url", *baseURL, "id", reqID, "anubis_image", *anubisImage, "chrome_versions", chromeVs, "firefox_versions", firefoxVs, "policies", len(policies))
```

- [ ] **Step 6: Verify it builds and fails loudly on a bad dir**

```bash
go build -o ./var/gubalctl ./cmd/gubalctl
./var/gubalctl -url http://example.invalid -anubis-image x -access-key-id k -secret-access-key s -policy-dir /nonexistent 2>&1 | head -2
```

Expected: a JSON line whose `err` mentions `reading policy dir /nonexistent`, exiting non-zero before any network call.

- [ ] **Step 7: Run the full suite**

Run: `go test ./... && go vet ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add cmd/gubalctl/
git commit -m "feat(gubalctl): submit the policy set from -policy-dir

Reads test/gubal/*.yaml into the name -> ruleset map gubald now requires,
without importing chromesweep (which would link client-go into the client)."
```

---

### Task 5: Per-policy bundle subfolders and a results table that links into them

**Files:**
- Modify: `chromesweep/bundle.go` (add path helpers, use them in `WriteBundle`, drop the `path/filepath` import)
- Modify: `chromesweep/report.go:150-155` (frame column)
- Test: `chromesweep/bundle_test.go` (update expected paths, add the collision case)
- Test: `chromesweep/report_test.go` (frame column assertion)

**Interfaces:**
- Consumes: `Result{Policy, Browser, Tag, FramePath, Logs}` (existing).
- Produces:
  - `func (r Result) BundleFramePath() string` — `frames/<policy>/<browser>-<tag>.png`, `""` when no frame.
  - `func (r Result) BundleLogPath(container string) string` — `logs/<policy>/<browser>-<tag>-<container>.log`.
  - Both omit the `<policy>/` segment when `Policy` is `""`.

- [ ] **Step 1: Write the failing tests**

In `chromesweep/bundle_test.go`, update `TestWriteBundle`'s results to carry a browser and policy, and assert the new paths. Replace the `results` literal and the two frame assertions:

```go
	results := []Result{
		{Policy: "fast", Browser: "chrome", Tag: "150", Status: StatusPass, FramePath: f150},
		{Policy: "fast", Browser: "chrome", Tag: "120", Status: StatusPass, FramePath: f120},
		{Policy: "fast", Browser: "chrome", Tag: "999", Status: StatusFail}, // no frame — must be skipped
	}
```

```go
	if !bytes.Equal(got["frames/fast/chrome-150.png"], []byte("PNG-150")) {
		t.Fatalf("frames/fast/chrome-150.png = %q", got["frames/fast/chrome-150.png"])
	}
	if !bytes.Equal(got["frames/fast/chrome-120.png"], []byte("PNG-120")) {
		t.Fatalf("frames/fast/chrome-120.png = %q", got["frames/fast/chrome-120.png"])
	}
```

In `TestWriteBundleIncludesLogs`, add a policy to the results and update the three log assertions:

```go
	results := []Result{
		{
			Policy:    "fast",
			Browser:   "firefox",
			Tag:       "152",
			Status:    StatusFail,
			FramePath: frame,
			Logs: []LogCapture{
				{Container: "firefox", Content: "bidi client closed"},
				{Container: "chrome-bully", Content: "fatal: loading url"},
				{Container: "smoke", Content: ""}, // empty — must be skipped
			},
		},
		{Policy: "fast", Browser: "chrome", Tag: "150", Status: StatusPass}, // no logs, no frame
	}
```

```go
	if !bytes.Equal(got["logs/fast/firefox-152-firefox.log"], []byte("bidi client closed")) {
		t.Fatalf("firefox log = %q", got["logs/fast/firefox-152-firefox.log"])
	}
	if !bytes.Equal(got["logs/fast/firefox-152-chrome-bully.log"], []byte("fatal: loading url")) {
		t.Fatalf("chrome-bully log = %q", got["logs/fast/firefox-152-chrome-bully.log"])
	}
	if _, ok := got["logs/fast/firefox-152-smoke.log"]; ok {
		t.Fatal("empty log content must be skipped")
	}
```

Then append the two new tests — the collision this change exists to fix, and the empty-policy fallback:

```go
// TestWriteBundleSeparatesPolicies is the reason bundles gained subfolders: the
// same browser+tag under two policies must not collide on one zip entry name.
func TestWriteBundleSeparatesPolicies(t *testing.T) {
	dir := t.TempDir()
	frame := filepath.Join(dir, "chrome-150.png")
	if err := os.WriteFile(frame, []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	results := []Result{
		{Policy: "default-config", Browser: "chrome", Tag: "150", Status: StatusFail, FramePath: frame,
			Logs: []LogCapture{{Container: "chrome-bully", Content: "under default-config"}}},
		{Policy: "fast", Browser: "chrome", Tag: "150", Status: StatusFail, FramePath: frame,
			Logs: []LogCapture{{Container: "chrome-bully", Content: "under fast"}}},
	}
	zipPath := filepath.Join(dir, "report.zip")
	if err := WriteBundle(zipPath, []byte("{}"), []byte("# report\n"), results); err != nil {
		t.Fatal(err)
	}

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	got := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatal(err)
		}
		rc.Close()
		if _, dup := got[f.Name]; dup {
			t.Fatalf("duplicate zip entry %q", f.Name)
		}
		got[f.Name] = buf.Bytes()
	}

	if !bytes.Equal(got["logs/default-config/chrome-150-chrome-bully.log"], []byte("under default-config")) {
		t.Fatalf("default-config log = %q", got["logs/default-config/chrome-150-chrome-bully.log"])
	}
	if !bytes.Equal(got["logs/fast/chrome-150-chrome-bully.log"], []byte("under fast")) {
		t.Fatalf("fast log = %q", got["logs/fast/chrome-150-chrome-bully.log"])
	}
}

func TestBundlePathsWithoutPolicy(t *testing.T) {
	// Anubis's live ruleset produces an empty Policy; the subfolder is omitted.
	r := Result{Browser: "chrome", Tag: "150", FramePath: "/tmp/x/chrome-150.png"}
	if got := r.BundleFramePath(); got != "frames/chrome-150.png" {
		t.Fatalf("BundleFramePath = %q", got)
	}
	if got := r.BundleLogPath("smoke"); got != "logs/chrome-150-smoke.log" {
		t.Fatalf("BundleLogPath = %q", got)
	}
	if got := (Result{Browser: "chrome", Tag: "150"}).BundleFramePath(); got != "" {
		t.Fatalf("a result with no frame must yield %q, got %q", "", got)
	}
}
```

In `chromesweep/report_test.go`, add this test (the fixtures at lines 13 and 15 already use `Policy: "default"` with `FramePath: "var/sweep/default-chrome-150.png"`):

```go
// TestRenderMarkdownLinksIntoBundle checks the frame column names a path that
// exists inside report.zip, not the scratch dir the sweep happened to use.
func TestRenderMarkdownLinksIntoBundle(t *testing.T) {
	md := RenderMarkdown(Report{Results: []Result{
		{Policy: "default", Browser: "chrome", Tag: "150", Status: StatusPass, FramePath: "/tmp/sweep-123/default-chrome-150.png"},
	}})
	if !strings.Contains(md, "frames/default/chrome-150.png") {
		t.Fatalf("report should link the bundle-relative frame path:\n%s", md)
	}
	if strings.Contains(md, "/tmp/sweep-123") {
		t.Fatalf("report must not leak the local scratch path:\n%s", md)
	}
}
```

If `report_test.go` does not already import `strings`, add it.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./chromesweep/ -run 'TestWriteBundle|TestBundlePaths|TestRenderMarkdownLinksIntoBundle' -v`
Expected: FAIL — `undefined: BundleFramePath` / `undefined: BundleLogPath` (build error).

- [ ] **Step 3: Add the path helpers and use them in `WriteBundle`**

In `chromesweep/bundle.go`, drop `"path/filepath"` from the imports (only `filepath.Base` used it):

```go
import (
	"archive/zip"
	"fmt"
	"os"
)
```

Replace the per-result loop body inside `WriteBundle`:

```go
	for _, r := range results {
		if r.FramePath != "" {
			data, rerr := os.ReadFile(r.FramePath)
			if rerr != nil {
				return fmt.Errorf("reading frame %s: %w", r.FramePath, rerr)
			}
			if err := addZipFile(zw, r.BundleFramePath(), data); err != nil {
				return err
			}
		}
		for _, lg := range r.Logs {
			if lg.Content == "" {
				continue
			}
			if err := addZipFile(zw, r.BundleLogPath(lg.Container), []byte(lg.Content)); err != nil {
				return err
			}
		}
	}
```

Add the helpers at the end of `bundle.go` — they live here because this file owns bundle layout:

```go
// BundleFramePath returns the frame's path inside the bundle WriteBundle
// produces, or "" when no frame was captured. Reports link to this rather than
// FramePath, which points into a scratch dir that does not outlive the sweep.
func (r Result) BundleFramePath() string {
	if r.FramePath == "" {
		return ""
	}
	return bundlePath("frames", r.Policy, fmt.Sprintf("%s-%s.png", r.Browser, r.Tag))
}

// BundleLogPath returns the given container log's path inside the bundle.
func (r Result) BundleLogPath(container string) string {
	return bundlePath("logs", r.Policy, fmt.Sprintf("%s-%s-%s.log", r.Browser, r.Tag, container))
}

// bundlePath joins a bundle top-level dir, an optional policy subfolder, and a
// leaf name. Policy is what keeps the same browser+tag from colliding across
// passes; an empty policy (Anubis's live ruleset) omits the subfolder. Always
// forward slashes: these are zip entry names, not OS paths.
func bundlePath(kind, policy, leaf string) string {
	if policy == "" {
		return kind + "/" + leaf
	}
	return kind + "/" + policy + "/" + leaf
}
```

- [ ] **Step 4: Point the results table at the bundle**

In `chromesweep/report.go`, change the table row in `renderBrowserGroups` (line 153-154) from `dash(r.FramePath)` to `dash(r.BundleFramePath())`:

```go
			fmt.Fprintf(b, "| %s | %s | %s | %s | %s |\n",
				r.Tag, r.Status, dash(r.BrowserVersion), dash(r.BundleFramePath()), dash(r.Detail))
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./chromesweep/ -v && go vet ./chromesweep/`
Expected: PASS, including `TestWriteBundleSeparatesPolicies`.

- [ ] **Step 6: Commit**

```bash
git add chromesweep/bundle.go chromesweep/bundle_test.go chromesweep/report.go chromesweep/report_test.go
git commit -m "fix(chromesweep): namespace bundle artifacts by policy

Frames and logs move into frames/<policy>/ and logs/<policy>/, so a
multi-policy sweep no longer writes two zip entries with the same name and
silently drops one. The results table now links the bundle-relative frame
path instead of an absolute scratch path that means nothing to a reader."
```

---

### Task 6: Forward chrome-bully's devtools console to slog

**Files:**
- Create: `cmd/chrome-bully/console.go`
- Test: `cmd/chrome-bully/console_test.go`
- Modify: `cmd/chrome-bully/main.go:178-194` (`capture`: init tab, attach listener, enable domains)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `func listenConsole(ctx context.Context)` — attaches the CDP event listener.
  - `func consoleActions() []chromedp.Action` — the domain-enable actions to run before navigating.
  - `func consoleLevel(s string) slog.Level`, `func consoleArgs(args []*runtime.RemoteObject) string` (internal, unit-tested).

Console lines use `msg: "browser console"`, deliberately distinct from the `captured` and `fatal` messages `chromesweep/parse.go:48` keys on, so they can never be read as result markers.

- [ ] **Step 1: Write the failing test**

Create `cmd/chrome-bully/console_test.go`:

```go
package main

import (
	"log/slog"
	"testing"

	"github.com/chromedp/cdproto/runtime"
)

func TestConsoleLevel(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want slog.Level
	}{
		{"error", slog.LevelError},
		{"assert", slog.LevelError},
		{"warning", slog.LevelWarn}, // Runtime domain spelling
		{"warn", slog.LevelWarn},
		{"debug", slog.LevelDebug},
		{"verbose", slog.LevelDebug}, // Log domain spelling
		{"trace", slog.LevelDebug},
		{"log", slog.LevelInfo},
		{"info", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"ERROR", slog.LevelError}, // case-insensitive
	} {
		if got := consoleLevel(tt.in); got != tt.want {
			t.Errorf("consoleLevel(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestConsoleArgs(t *testing.T) {
	args := []*runtime.RemoteObject{
		{Type: "string", Value: []byte(`"challenge solved"`)}, // JSON-quoted; must be unquoted
		{Type: "number", Value: []byte(`42`)},
		{Type: "object", Description: "Error: boom"},           // no value; falls back to description
		{Type: "number", UnserializableValue: "NaN"},           // neither; falls back to the marker
		nil,                                                    // must be skipped, not panic
	}
	if got, want := consoleArgs(args), "challenge solved 42 Error: boom NaN"; got != want {
		t.Fatalf("consoleArgs = %q, want %q", got, want)
	}
	if got := consoleArgs(nil); got != "" {
		t.Fatalf("consoleArgs(nil) = %q, want empty", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/chrome-bully/ -run 'TestConsoleLevel|TestConsoleArgs' -v`
Expected: FAIL — `undefined: consoleLevel`, `undefined: consoleArgs`.

- [ ] **Step 3: Implement the console forwarder**

Create `cmd/chrome-bully/console.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	cdplog "github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// consoleMsg is the slog message every devtools-console line shares. It is
// deliberately distinct from "captured" and "fatal", the two messages
// chromesweep's log parser keys on, so console output is never mistaken for a
// result marker.
const consoleMsg = "browser console"

// consoleActions enables the CDP domains listenConsole draws from: Runtime
// carries console API calls and uncaught exceptions, Log carries browser-level
// entries (network failures, CSP/security violations, deprecations). Run these
// before navigating.
func consoleActions() []chromedp.Action {
	return []chromedp.Action{runtime.Enable(), cdplog.Enable()}
}

// listenConsole forwards the tab's devtools console to slog, so a version that
// fails its Anubis challenge leaves the JS/WASM errors explaining why in the pod
// log (and therefore in the report bundle).
//
// Attach it after the target exists but before navigating, or messages from the
// first paint are lost. The callback runs on chromedp's event goroutine, so it
// must not block; slog is safe for concurrent use.
func listenConsole(ctx context.Context) {
	chromedp.ListenTarget(ctx, func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			slog.Log(context.Background(), consoleLevel(string(e.Type)), consoleMsg,
				"kind", "console", "type", string(e.Type), "text", consoleArgs(e.Args))
		case *runtime.EventExceptionThrown:
			d := e.ExceptionDetails
			if d == nil {
				return
			}
			slog.Error(consoleMsg,
				"kind", "exception", "text", d.Text, "detail", exceptionDetail(d),
				"url", d.URL, "line", d.LineNumber)
		case *cdplog.EventEntryAdded:
			if e.Entry == nil {
				return
			}
			en := e.Entry
			slog.Log(context.Background(), consoleLevel(string(en.Level)), consoleMsg,
				"kind", "log-entry", "source", string(en.Source), "level", string(en.Level),
				"text", en.Text, "url", en.URL, "line", en.LineNumber)
		}
	})
}

// consoleLevel maps a CDP severity onto a slog level. The Runtime and Log
// domains spell these differently ("warning" vs "warn", "verbose" vs "debug"),
// so both spellings are accepted.
func consoleLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "error", "assert":
		return slog.LevelError
	case "warning", "warn":
		return slog.LevelWarn
	case "debug", "verbose", "trace":
		return slog.LevelDebug
	default:
		return slog.LevelInfo
	}
}

// consoleArgs flattens a console call's arguments into one line.
func consoleArgs(args []*runtime.RemoteObject) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		if a == nil {
			continue
		}
		parts = append(parts, remoteObject(a))
	}
	return strings.Join(parts, " ")
}

// remoteObject renders one console argument as text. A RemoteObject carries
// either a JSON value (primitives), a description (objects and errors), or an
// unserializable marker (NaN, Infinity); prefer them in that order.
func remoteObject(a *runtime.RemoteObject) string {
	if len(a.Value) > 0 {
		// Strings arrive JSON-quoted; unquote them so the log reads naturally.
		var s string
		if err := json.Unmarshal([]byte(a.Value), &s); err == nil {
			return s
		}
		return string(a.Value)
	}
	if a.Description != "" {
		return a.Description
	}
	if a.UnserializableValue != "" {
		return string(a.UnserializableValue)
	}
	return string(a.Type)
}

// exceptionDetail prefers the exception object's description, which carries the
// stack, over the bare summary text.
func exceptionDetail(d *runtime.ExceptionDetails) string {
	if d.Exception == nil {
		return ""
	}
	return d.Exception.Description
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/chrome-bully/ -run 'TestConsoleLevel|TestConsoleArgs' -v`
Expected: PASS.

- [ ] **Step 5: Wire the listener into `capture`**

In `cmd/chrome-bully/main.go`, in `capture`, replace the tab setup so the listener is attached before anything navigates:

```go
	tabCtx, cancelTab := chromedp.NewContext(ctx)
	defer cancelTab()

	// ListenTarget needs a live target, and chromedp creates one lazily on the
	// first Run. An empty Run forces it into existence so the console listener is
	// attached before the page loads and can see its first messages.
	if err := chromedp.Run(tabCtx); err != nil {
		return "", fmt.Errorf("opening tab: %w", err)
	}
	listenConsole(tabCtx)

	// Enable the console domains, set extra headers (needs the Network domain)
	// and viewport, then load.
	setup := make([]chromedp.Action, 0, 6)
	setup = append(setup, consoleActions()...)
	if len(extraHeaders) > 0 {
		setup = append(setup, network.Enable(), network.SetExtraHTTPHeaders(extraHeaders))
	}
```

Leave the rest of `capture` (viewport, `Navigate`, `waitForText`, screenshot, write) unchanged.

- [ ] **Step 6: Verify it builds and the whole suite passes**

```bash
go build -o ./var/chrome-bully ./cmd/chrome-bully
go test ./... && go vet ./...
```

Expected: builds clean, all tests PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/chrome-bully/
git commit -m "feat(chrome-bully): log the devtools console

Forwards console API calls, uncaught exceptions, and browser-level Log
entries to slog under msg=\"browser console\", so a failed Anubis challenge
leaves its JS/WASM errors in the pod log and the report bundle. The distinct
msg keeps chromesweep's captured/fatal log parsing untouched."
```

---

### Task 7: Update the repo docs

**Files:**
- Modify: `CLAUDE.md` (policy location and the runtime-file conventions)
- Modify: `cmd/chrome-sweep/README.md` (document `-policy-dir`)

**Interfaces:**
- Consumes: everything above.
- Produces: nothing.

- [ ] **Step 1: Correct the CLAUDE.md conventions**

In the "Non-obvious cross-cutting conventions" section, extend the bullet about `chromesweep` reading manifests from disk so it also covers policies:

```markdown
- **`chromesweep` reads the `k8s/*.yaml` manifests from disk at runtime**, relative
  to the working directory (`DefaultConfig` paths like `k8s/deployment.yaml`). Any
  image running it (e.g. `gubald`) must ship those files under its `WORKDIR` **and**
  include a `kubectl` binary — `chromesweep.CopyFrame` shells out to `kubectl cp`.
- **Anubis rulesets live in `test/gubal/*.yaml` and are no longer embedded.**
  `DefaultConfig().Policies` is nil; callers fill it. `chrome-sweep` loads the
  directory via `-policy-dir`, and `gubalctl` reads the same directory and submits
  it as the required `policies` map on `SmokeTestRequest`, so `gubald` needs no
  policy files on disk. An empty map is rejected — there is no server-side default.
```

- [ ] **Step 2: Document the flag in the chrome-sweep README**

Add to the flag list in `cmd/chrome-sweep/README.md`:

```markdown
- `-policy-dir` (default `test/gubal`) — directory of Anubis `botPolicies`
  `*.yaml` rulesets. Every browser version is swept once per ruleset, and the
  filename without its extension names the pass. A missing or ruleset-free
  directory is a fatal error.
```

- [ ] **Step 3: Verify nothing stale remains**

```bash
grep -rn "go:embed" chromesweep/ CLAUDE.md
grep -rn "chromesweep/policies\|embedded polic" . --exclude-dir=.git --exclude-dir=var --exclude-dir=docs
```

Expected: no hits. Every reference to embedded policies or `chromesweep/policies/` is gone. (`docs/` is excluded because the spec and this plan describe the old state on purpose.)

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md cmd/chrome-sweep/README.md
git commit -m "docs: record that policies load from test/gubal, not go:embed"
```

---

## Final Verification

- [ ] **Run the whole suite with the race detector**

```bash
go test -race ./... && go vet ./...
```

Expected: PASS. The race detector matters for Task 6: the console listener writes from chromedp's event goroutine.

- [ ] **Build every binary into ./var**

```bash
for c in chrome-sweep gubald gubalctl chrome-bully; do go build -o ./var/$c ./cmd/$c || echo "FAILED: $c"; done
```

Expected: no output, four binaries in `./var`, none in the repo root.

- [ ] **Confirm the proto is regenerated and committed**

```bash
buf lint && buf generate && git status --short gen/
```

Expected: `buf lint` silent, `git status` clean — the committed generated code matches the proto.
