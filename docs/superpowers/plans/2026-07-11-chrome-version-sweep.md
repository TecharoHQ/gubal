# Chrome Version Sweep Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `cmd/chrome-sweep`, a Go tool that tests a list of Chrome image versions one after another by re-pointing the in-cluster `chrome` Deployment at each tag, running the existing smoke Job against it, and collecting a pass/fail + screenshot per version.

**Architecture:** Anubis, relayd, httpdebug, the PVC, and the smoke Job are all version-independent and stay up; only the `chrome` Deployment's image tag changes between runs. The tool loops over version tags: patch the Deployment image (client-go) → wait for rollout Ready → replace and run the `chrome-smoke` Job → wait for Complete/Failed → read the Job pod logs → `kubectl cp` the captured frame off the PVC → record the result. At the end it writes a Markdown table and a JSON file.

**Tech Stack:** Go 1.26, `k8s.io/client-go` (control plane operations), `sigs.k8s.io/yaml` (decode the Job manifest), `kubectl cp` (frame retrieval only). No new runtime services.

## Global Constraints

Copy these exact values; every task depends on them.

- Go module path: `github.com/TecharoHQ/gubal`
- Go version: `go 1.26.4`
- Build binaries into `./var`, never the repo root (e.g. `go build -o ./var/chrome-sweep ./cmd/chrome-sweep`).
- Kubernetes namespace: `ci`
- Deployment name: `chrome`; the container to re-image is named `chrome`
- Image repository: `ghcr.io/techarohq/gubal/chrome` (final image ref is `<repo>:<tag>`)
- Smoke Job name: `chrome-smoke`; reused verbatim from `k8s/smoke-job.yaml` (do not fork it)
- PVC holding frames: `chrome-bully-data`, mounted at `/data`
- `chrome-bully` logs the frame it wrote as a JSON line: `{"level":"INFO","msg":"captured","path":"/data/chrome-<version>-<ts>.png"}`; on failure it logs `{"level":"ERROR","msg":"fatal","err":"..."}`
- The `smoke` container logs `User-Agent: <ua>` and `PASS: driveable, non-Headless UA`
- Prerequisites that MUST already exist in `ci` before a sweep: the Anubis deployment (`k8s/anubis`), the `anubis-key` secret, the `chrome` Deployment/Service, and the `chrome-bully-data` PVC. The tool does not create these.
- Commit style: Conventional Commits (`feat(...)`, `test(...)`, etc.)

---

## File Structure

- `cmd/chrome-sweep/main.go` — flag parsing, kube client bootstrap, wiring, writing report files.
- `cmd/chrome-sweep/parse.go` — pure helpers: `parseVersions`, `reportedUA`, `capturedFramePath`.
- `cmd/chrome-sweep/parse_test.go` — table tests for the pure helpers.
- `cmd/chrome-sweep/report.go` — `Result`, `Status`, `renderMarkdown`, `renderJSON`.
- `cmd/chrome-sweep/report_test.go` — table tests for rendering.
- `cmd/chrome-sweep/cluster.go` — `Cluster` type wrapping client-go: image patch, rollout wait, job replace/wait, pod logs, collector pod, `kubectl cp`.
- `cmd/chrome-sweep/cluster_test.go` — fake-clientset tests for the pure-ish client-go operations.
- `cmd/chrome-sweep/sweep.go` — `Sweeper` + `Run`: the per-version orchestration loop.
- `cmd/chrome-sweep/README.md` — usage.

Test IDs and reported UA are parsed from logs, so the log-parsing helpers are the testable core; the client-go waits are covered by a documented real-cluster run in the final task.

---

### Task 1: Scaffold the command, dependencies, and flags

**Files:**
- Create: `cmd/chrome-sweep/main.go`
- Modify: `go.mod`, `go.sum` (via `go get`)

**Interfaces:**
- Consumes: nothing.
- Produces: `type Config struct` with fields used by later tasks (see Step 3); `func kubeClient(kubeconfig string) (kubernetes.Interface, error)`.

- [ ] **Step 1: Add the Kubernetes client dependencies**

Run:
```bash
cd /home/xe/code/TecharoHQ/gubal
go get k8s.io/client-go@v0.31.1 k8s.io/api@v0.31.1 k8s.io/apimachinery@v0.31.1 sigs.k8s.io/yaml@v1.4.0
```
Expected: `go.mod`/`go.sum` gain the modules, no error. (If `v0.31.1` fails to resolve under Go 1.26, use `@latest` and run `go mod tidy`.)

- [ ] **Step 2: Write the flags-and-wiring skeleton**

Create `cmd/chrome-sweep/main.go`:
```go
// Command chrome-sweep tests a list of Chrome image tags one after another: it
// re-points the in-cluster `chrome` Deployment at each tag, runs the existing
// chrome-smoke Job against it, and records a pass/fail + screenshot per version.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Config is the fully-resolved run configuration.
type Config struct {
	Namespace    string
	Deployment   string
	Container    string
	ImageRepo    string
	JobManifest  string
	JobName      string
	CollectorPVC string
	OutDir       string
	ReadyTimeout time.Duration
	JobTimeout   time.Duration
	Versions     []string
}

func main() {
	var (
		kubeconfig = flag.String("kubeconfig", defaultKubeconfig(), "path to kubeconfig")
		cfg        = Config{}
	)
	flag.StringVar(&cfg.Namespace, "namespace", "ci", "namespace holding the chrome Deployment and smoke Job")
	flag.StringVar(&cfg.Deployment, "deployment", "chrome", "Deployment to re-image per version")
	flag.StringVar(&cfg.Container, "container", "chrome", "container within the Deployment to re-image")
	flag.StringVar(&cfg.ImageRepo, "image-repo", "ghcr.io/techarohq/gubal/chrome", "image repository; final ref is <repo>:<tag>")
	flag.StringVar(&cfg.JobManifest, "job-manifest", "k8s/smoke-job.yaml", "path to the smoke Job manifest to run each version")
	flag.StringVar(&cfg.JobName, "job-name", "chrome-smoke", "metadata.name of the Job in the manifest")
	flag.StringVar(&cfg.CollectorPVC, "pvc", "chrome-bully-data", "PVC that holds captured frames")
	flag.StringVar(&cfg.OutDir, "out", "./var/sweep", "directory to write the report and copied frames into")
	flag.DurationVar(&cfg.ReadyTimeout, "ready-timeout", 3*time.Minute, "max wait for a version's rollout to become Ready")
	flag.DurationVar(&cfg.JobTimeout, "job-timeout", 4*time.Minute, "max wait for the smoke Job to finish")
	flag.Parse()

	cfg.Versions = flag.Args()

	if err := run(*kubeconfig, cfg); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func defaultKubeconfig() string {
	if v := os.Getenv("KUBECONFIG"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kube", "config")
}

// kubeClient builds a clientset from a kubeconfig path.
func kubeClient(kubeconfig string) (kubernetes.Interface, error) {
	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig %q: %w", kubeconfig, err)
	}
	return kubernetes.NewForConfig(restCfg)
}

// run is filled in in Task 7; for now it validates wiring.
func run(kubeconfig string, cfg Config) error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if len(cfg.Versions) == 0 {
		return fmt.Errorf("no versions given; usage: chrome-sweep [flags] TAG [TAG...]")
	}
	cs, err := kubeClient(kubeconfig)
	if err != nil {
		return err
	}
	_ = cs
	_ = context.Background()
	slog.Info("configured", "namespace", cfg.Namespace, "versions", cfg.Versions, "out", cfg.OutDir)
	return nil
}
```

- [ ] **Step 3: Build and verify it wires up**

Run:
```bash
go build -o ./var/chrome-sweep ./cmd/chrome-sweep && ./var/chrome-sweep 2>&1 | head -1
```
Expected: build succeeds; running with no args prints a fatal log containing `no versions given`.

- [ ] **Step 4: Commit**

```bash
git add cmd/chrome-sweep/main.go go.mod go.sum
git commit -m "feat(chrome-sweep): scaffold command, deps, and flags"
```

---

### Task 2: Version list parsing

**Files:**
- Create: `cmd/chrome-sweep/parse.go`
- Test: `cmd/chrome-sweep/parse_test.go`

**Interfaces:**
- Produces: `func parseVersions(args []string) ([]string, error)` — trims whitespace, drops empties, rejects duplicates, errors when the result is empty.

- [ ] **Step 1: Write the failing test**

Create `cmd/chrome-sweep/parse_test.go`:
```go
package main

import (
	"reflect"
	"testing"
)

func TestParseVersions(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    []string
		wantErr bool
	}{
		{"basic", []string{"110", "120", "150"}, []string{"110", "120", "150"}, false},
		{"trims and drops empties", []string{" 110 ", "", "120"}, []string{"110", "120"}, false},
		{"rejects duplicates", []string{"110", "110"}, nil, true},
		{"empty is an error", []string{"", "  "}, nil, true},
		{"no args is an error", nil, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVersions(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/chrome-sweep/ -run TestParseVersions -v`
Expected: FAIL — `undefined: parseVersions`.

- [ ] **Step 3: Write minimal implementation**

Create `cmd/chrome-sweep/parse.go`:
```go
package main

import (
	"fmt"
	"strings"
)

// parseVersions trims and de-empties the given tags, rejects duplicates, and
// errors if nothing usable remains.
func parseVersions(args []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(args))
	for _, a := range args {
		t := strings.TrimSpace(a)
		if t == "" {
			continue
		}
		if seen[t] {
			return nil, fmt.Errorf("duplicate version %q", t)
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no versions given")
	}
	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/chrome-sweep/ -run TestParseVersions -v`
Expected: PASS.

- [ ] **Step 5: Wire it into `run` and commit**

In `cmd/chrome-sweep/main.go`, replace `cfg.Versions = flag.Args()` with:
```go
	versions, err := parseVersions(flag.Args())
	if err != nil {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
		slog.Error("bad versions", "err", err)
		os.Exit(1)
	}
	cfg.Versions = versions
```
Then remove the now-redundant `len(cfg.Versions) == 0` check from `run`.

Run: `go build -o ./var/chrome-sweep ./cmd/chrome-sweep && go test ./cmd/chrome-sweep/`
Expected: build + tests pass.

```bash
git add cmd/chrome-sweep/parse.go cmd/chrome-sweep/parse_test.go cmd/chrome-sweep/main.go
git commit -m "feat(chrome-sweep): parse and validate version arguments"
```

---

### Task 3: Log parsing (reported UA + captured frame path)

**Files:**
- Modify: `cmd/chrome-sweep/parse.go`
- Modify: `cmd/chrome-sweep/parse_test.go`

**Interfaces:**
- Produces:
  - `func reportedUA(smokeLogs string) string` — returns the value after the last `User-Agent: ` line, or `""`.
  - `func capturedFramePath(bullyLogs string) (string, error)` — scans `chrome-bully`'s JSON log lines; returns the `path` from the `captured` line, or an error carrying the `fatal` line's `err`, or an error if neither is present.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/chrome-sweep/parse_test.go`:
```go
func TestReportedUA(t *testing.T) {
	logs := "waiting for chrome:9222 ...\nUser-Agent: Mozilla/5.0 Chrome/150.0.0.0 Safari/537.36\nPASS: driveable, non-Headless UA\n"
	if got, want := reportedUA(logs), "Mozilla/5.0 Chrome/150.0.0.0 Safari/537.36"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := reportedUA("no ua here"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestCapturedFramePath(t *testing.T) {
	ok := `{"level":"INFO","msg":"connected to chrome"}
{"level":"INFO","msg":"captured","path":"/data/chrome-150.0.7871.114-20260101T000000.000Z.png"}`
	got, err := capturedFramePath(ok)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if want := "/data/chrome-150.0.7871.114-20260101T000000.000Z.png"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	fatal := `{"level":"INFO","msg":"connected to chrome"}
{"level":"ERROR","msg":"fatal","err":"timed out waiting for text"}`
	if _, err := capturedFramePath(fatal); err == nil {
		t.Fatalf("expected error from fatal log")
	}

	if _, err := capturedFramePath("garbage\n"); err == nil {
		t.Fatalf("expected error when no capture line present")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/chrome-sweep/ -run 'TestReportedUA|TestCapturedFramePath' -v`
Expected: FAIL — `undefined: reportedUA` / `capturedFramePath`.

- [ ] **Step 3: Write the implementation**

Add to `cmd/chrome-sweep/parse.go` (add `"bufio"`, `"encoding/json"` to imports):
```go
// reportedUA returns the User-Agent value the smoke container logged, or "".
func reportedUA(smokeLogs string) string {
	const marker = "User-Agent: "
	ua := ""
	sc := bufio.NewScanner(strings.NewReader(smokeLogs))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, marker); i >= 0 {
			ua = strings.TrimSpace(line[i+len(marker):])
		}
	}
	return ua
}

// capturedFramePath scans chrome-bully's JSON log lines for the frame it wrote,
// returning that path. A "fatal" line becomes an error; absence of both is an error.
func capturedFramePath(bullyLogs string) (string, error) {
	type logLine struct {
		Msg  string `json:"msg"`
		Path string `json:"path"`
		Err  string `json:"err"`
	}
	sc := bufio.NewScanner(strings.NewReader(bullyLogs))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var l logLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			continue // non-JSON line; skip
		}
		switch l.Msg {
		case "captured":
			if l.Path != "" {
				return l.Path, nil
			}
		case "fatal":
			return "", fmt.Errorf("chrome-bully failed: %s", l.Err)
		}
	}
	return "", fmt.Errorf("no captured frame in chrome-bully logs")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/chrome-sweep/ -run 'TestReportedUA|TestCapturedFramePath' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/chrome-sweep/parse.go cmd/chrome-sweep/parse_test.go
git commit -m "feat(chrome-sweep): parse UA and captured-frame path from job logs"
```

---

### Task 4: Result model and report rendering

**Files:**
- Create: `cmd/chrome-sweep/report.go`
- Test: `cmd/chrome-sweep/report_test.go`

**Interfaces:**
- Produces:
  - `type Status string` with consts `StatusPass`, `StatusFail`, `StatusNotReady`, `StatusError`.
  - `type Result struct { Tag, ChromeVersion, ReportedUA, FramePath, Detail string; Status Status }` with JSON tags.
  - `func renderMarkdown(results []Result) string`
  - `func renderJSON(results []Result) ([]byte, error)`

- [ ] **Step 1: Write the failing test**

Create `cmd/chrome-sweep/report_test.go`:
```go
package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	md := renderMarkdown([]Result{
		{Tag: "150", Status: StatusPass, ChromeVersion: "150.0.7871.114", ReportedUA: "Chrome/150", FramePath: "var/sweep/150.png"},
		{Tag: "110", Status: StatusFail, Detail: "job failed"},
	})
	for _, want := range []string{"| 150 |", "pass", "| 110 |", "fail", "job failed", "1/2 passed"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestRenderJSON(t *testing.T) {
	b, err := renderJSON([]Result{{Tag: "150", Status: StatusPass}})
	if err != nil {
		t.Fatal(err)
	}
	var out []Result
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(out) != 1 || out[0].Tag != "150" || out[0].Status != StatusPass {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/chrome-sweep/ -run 'TestRender' -v`
Expected: FAIL — `undefined: renderMarkdown` etc.

- [ ] **Step 3: Write the implementation**

Create `cmd/chrome-sweep/report.go`:
```go
package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Status string

const (
	StatusPass     Status = "pass"
	StatusFail     Status = "fail"
	StatusNotReady Status = "not-ready"
	StatusError    Status = "error"
)

// Result is the outcome of testing one Chrome tag.
type Result struct {
	Tag           string `json:"tag"`
	Status        Status `json:"status"`
	ChromeVersion string `json:"chrome_version,omitempty"`
	ReportedUA    string `json:"reported_ua,omitempty"`
	FramePath     string `json:"frame_path,omitempty"`
	Detail        string `json:"detail,omitempty"`
}

// renderMarkdown produces a human-readable summary table.
func renderMarkdown(results []Result) string {
	var b strings.Builder
	passed := 0
	for _, r := range results {
		if r.Status == StatusPass {
			passed++
		}
	}
	fmt.Fprintf(&b, "# Chrome version sweep — %d/%d passed\n\n", passed, len(results))
	b.WriteString("| tag | status | chrome version | frame | detail |\n")
	b.WriteString("|-----|--------|----------------|-------|--------|\n")
	for _, r := range results {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
			r.Tag, r.Status, dash(r.ChromeVersion), dash(r.FramePath), dash(r.Detail))
	}
	return b.String()
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// renderJSON serializes the results as indented JSON.
func renderJSON(results []Result) ([]byte, error) {
	return json.MarshalIndent(results, "", "  ")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/chrome-sweep/ -run 'TestRender' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/chrome-sweep/report.go cmd/chrome-sweep/report_test.go
git commit -m "feat(chrome-sweep): result model with markdown and json reports"
```

---

### Task 5: Cluster operations (client-go)

**Files:**
- Create: `cmd/chrome-sweep/cluster.go`
- Test: `cmd/chrome-sweep/cluster_test.go`

**Interfaces:**
- Produces a `Cluster` with these methods (used by Task 7):
  - `func NewCluster(cs kubernetes.Interface, namespace string) *Cluster`
  - `func (c *Cluster) SetImage(ctx context.Context, deployment, container, image string) error`
  - `func (c *Cluster) WaitDeploymentReady(ctx context.Context, deployment string, timeout time.Duration) error`
  - `func (c *Cluster) ReplaceJob(ctx context.Context, job *batchv1.Job) error`
  - `func (c *Cluster) WaitJob(ctx context.Context, name string, timeout time.Duration) (succeeded bool, err error)`
  - `func (c *Cluster) JobContainerLogs(ctx context.Context, jobName, container string) (string, error)`
  - `func loadJob(path, namespace string) (*batchv1.Job, error)`

- [ ] **Step 1: Write the failing tests (fake clientset)**

Create `cmd/chrome-sweep/cluster_test.go`:
```go
package main

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSetImage(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "chrome", Namespace: "ci"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "chrome", Image: "old:1"}}},
			},
		},
	}
	cs := fake.NewSimpleClientset(dep)
	c := NewCluster(cs, "ci")
	if err := c.SetImage(context.Background(), "chrome", "chrome", "repo:120"); err != nil {
		t.Fatal(err)
	}
	got, _ := cs.AppsV1().Deployments("ci").Get(context.Background(), "chrome", metav1.GetOptions{})
	if img := got.Spec.Template.Spec.Containers[0].Image; img != "repo:120" {
		t.Fatalf("image = %q, want repo:120", img)
	}
}

func TestReplaceJobCreatesWhenAbsent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	c := NewCluster(cs, "ci")
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "chrome-smoke", Namespace: "ci"}}
	if err := c.ReplaceJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.BatchV1().Jobs("ci").Get(context.Background(), "chrome-smoke", metav1.GetOptions{}); err != nil {
		t.Fatalf("job not created: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/chrome-sweep/ -run 'TestSetImage|TestReplaceJob' -v`
Expected: FAIL — `undefined: NewCluster`.

- [ ] **Step 3: Write the implementation**

Create `cmd/chrome-sweep/cluster.go`:
```go
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// Cluster wraps a clientset scoped to one namespace.
type Cluster struct {
	cs kubernetes.Interface
	ns string
}

func NewCluster(cs kubernetes.Interface, namespace string) *Cluster {
	return &Cluster{cs: cs, ns: namespace}
}

// SetImage strategic-merge-patches one container's image on a Deployment.
func (c *Cluster) SetImage(ctx context.Context, deployment, container, image string) error {
	patch := []byte(fmt.Sprintf(
		`{"spec":{"template":{"spec":{"containers":[{"name":%q,"image":%q}]}}}}`, container, image))
	_, err := c.cs.AppsV1().Deployments(c.ns).Patch(
		ctx, deployment, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patching %s image: %w", deployment, err)
	}
	return nil
}

// WaitDeploymentReady blocks until the Deployment's newest generation is fully
// rolled out and available, mirroring `kubectl rollout status`.
func (c *Cluster) WaitDeploymentReady(ctx context.Context, deployment string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			d, err := c.cs.AppsV1().Deployments(c.ns).Get(ctx, deployment, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			desired := int32(1)
			if d.Spec.Replicas != nil {
				desired = *d.Spec.Replicas
			}
			s := d.Status
			ready := d.Generation == s.ObservedGeneration &&
				s.UpdatedReplicas == desired &&
				s.AvailableReplicas == desired &&
				s.UnavailableReplicas == 0
			return ready, nil
		})
}

// ReplaceJob deletes any existing Job of the same name (waiting for it to fully
// disappear) and creates the given one.
func (c *Cluster) ReplaceJob(ctx context.Context, job *batchv1.Job) error {
	fg := metav1.DeletePropagationForeground
	err := c.cs.BatchV1().Jobs(c.ns).Delete(ctx, job.Name, metav1.DeleteOptions{PropagationPolicy: &fg})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting old job: %w", err)
	}
	if err == nil {
		if werr := wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true,
			func(ctx context.Context) (bool, error) {
				_, gerr := c.cs.BatchV1().Jobs(c.ns).Get(ctx, job.Name, metav1.GetOptions{})
				if apierrors.IsNotFound(gerr) {
					return true, nil
				}
				return false, nil
			}); werr != nil {
			return fmt.Errorf("waiting for old job to clear: %w", werr)
		}
	}
	job.Namespace = c.ns
	if _, err := c.cs.BatchV1().Jobs(c.ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating job: %w", err)
	}
	return nil
}

// WaitJob blocks until the Job reports Complete (succeeded=true) or Failed
// (succeeded=false). A timeout is returned as an error.
func (c *Cluster) WaitJob(ctx context.Context, name string, timeout time.Duration) (bool, error) {
	succeeded := false
	err := wait.PollUntilContextTimeout(ctx, 3*time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			j, err := c.cs.BatchV1().Jobs(c.ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			for _, cond := range j.Status.Conditions {
				if cond.Status != corev1.ConditionTrue {
					continue
				}
				switch cond.Type {
				case batchv1.JobComplete:
					succeeded = true
					return true, nil
				case batchv1.JobFailed:
					succeeded = false
					return true, nil
				}
			}
			return false, nil
		})
	return succeeded, err
}

// JobContainerLogs returns the logs of the named container in the newest pod
// belonging to the Job.
func (c *Cluster) JobContainerLogs(ctx context.Context, jobName, container string) (string, error) {
	pods, err := c.cs.CoreV1().Pods(c.ns).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + jobName,
	})
	if err != nil {
		return "", err
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods for job %s", jobName)
	}
	newest := pods.Items[0]
	for _, p := range pods.Items[1:] {
		if p.CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = p
		}
	}
	req := c.cs.CoreV1().Pods(c.ns).GetLogs(newest.Name, &corev1.PodLogOptions{Container: container})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	b, err := io.ReadAll(stream)
	return string(b), err
}

// loadJob reads a Job manifest from disk and pins its namespace.
func loadJob(path, namespace string) (*batchv1.Job, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var job batchv1.Job
	if err := yaml.Unmarshal(raw, &job); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", path, err)
	}
	job.Namespace = namespace
	return &job, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/chrome-sweep/ -run 'TestSetImage|TestReplaceJob' -v`
Expected: PASS. (`go vet ./cmd/chrome-sweep/` should also be clean.)

- [ ] **Step 5: Commit**

```bash
git add cmd/chrome-sweep/cluster.go cmd/chrome-sweep/cluster_test.go
git commit -m "feat(chrome-sweep): client-go operations for image, rollout, and job"
```

---

### Task 6: Frame collector via `kubectl cp`

**Files:**
- Modify: `cmd/chrome-sweep/cluster.go`

**Interfaces:**
- Produces:
  - `func (c *Cluster) EnsureCollector(ctx context.Context, name, pvc string, timeout time.Duration) error` — creates a busybox pod mounting the PVC read-only at `/data` and waits until it is Running.
  - `func (c *Cluster) DeleteCollector(ctx context.Context, name string) error`
  - `func (c *Cluster) CopyFrame(ctx context.Context, collector, remotePath, localPath string) error` — shells out to `kubectl cp`.

- [ ] **Step 1: Add the collector + copy methods**

Append to `cmd/chrome-sweep/cluster.go` (add `"os/exec"` and `"path/filepath"` to imports):
```go
// EnsureCollector creates (idempotently) a long-lived busybox pod that mounts the
// frames PVC read-only, so CopyFrame can pull files off it, and waits for Running.
func (c *Cluster) EnsureCollector(ctx context.Context, name, pvc string, timeout time.Duration) error {
	ro := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: c.ns},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "collector",
				Image:   "busybox:1.36",
				Command: []string{"sleep", "infinity"},
				VolumeMounts: []corev1.VolumeMount{{
					Name: "frames", MountPath: "/data", ReadOnly: true,
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "frames",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvc, ReadOnly: ro,
					},
				},
			}},
		},
	}
	_, err := c.cs.CoreV1().Pods(c.ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating collector: %w", err)
	}
	return wait.PollUntilContextTimeout(ctx, time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			p, err := c.cs.CoreV1().Pods(c.ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			return p.Status.Phase == corev1.PodRunning, nil
		})
}

// DeleteCollector removes the collector pod.
func (c *Cluster) DeleteCollector(ctx context.Context, name string) error {
	err := c.cs.CoreV1().Pods(c.ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// CopyFrame copies remotePath out of the collector pod to localPath using
// `kubectl cp` (which needs `tar` in the pod — busybox provides it).
func (c *Cluster) CopyFrame(ctx context.Context, collector, remotePath, localPath string) error {
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	src := fmt.Sprintf("%s/%s:%s", c.ns, collector, remotePath)
	cmd := exec.CommandContext(ctx, "kubectl", "cp", src, localPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("kubectl cp %s: %w: %s", src, err, out)
	}
	return nil
}
```

- [ ] **Step 2: Build and vet**

Run: `go build -o ./var/chrome-sweep ./cmd/chrome-sweep && go vet ./cmd/chrome-sweep/`
Expected: clean. (No unit test here — `kubectl cp` and PVC mounts are exercised by the real run in Task 7.)

- [ ] **Step 3: Commit**

```bash
git add cmd/chrome-sweep/cluster.go
git commit -m "feat(chrome-sweep): collector pod and kubectl-cp frame retrieval"
```

---

### Task 7: The sweep loop, wiring, real-cluster verification, and README

**Files:**
- Create: `cmd/chrome-sweep/sweep.go`
- Modify: `cmd/chrome-sweep/main.go`
- Create: `cmd/chrome-sweep/README.md`

**Interfaces:**
- Consumes everything above.
- Produces: `func Run(ctx context.Context, c *Cluster, cfg Config) ([]Result, error)`.

- [ ] **Step 1: Write the sweep loop**

Create `cmd/chrome-sweep/sweep.go`:
```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
)

const collectorPodName = "chrome-sweep-collector"

// Run tests each version in cfg.Versions serially and returns one Result each.
// A failure on one version is recorded and the sweep continues.
func Run(ctx context.Context, c *Cluster, cfg Config) ([]Result, error) {
	job, err := loadJob(cfg.JobManifest, cfg.Namespace)
	if err != nil {
		return nil, err
	}
	if err := c.EnsureCollector(ctx, collectorPodName, cfg.CollectorPVC, cfg.ReadyTimeout); err != nil {
		return nil, err
	}
	defer func() {
		if derr := c.DeleteCollector(context.Background(), collectorPodName); derr != nil {
			slog.Warn("collector cleanup failed", "err", derr)
		}
	}()

	results := make([]Result, 0, len(cfg.Versions))
	for _, tag := range cfg.Versions {
		results = append(results, sweepOne(ctx, c, cfg, job, tag))
	}
	return results, nil
}

func sweepOne(ctx context.Context, c *Cluster, cfg Config, job *batchv1.Job, tag string) Result {
	image := fmt.Sprintf("%s:%s", cfg.ImageRepo, tag)
	res := Result{Tag: tag}
	log := slog.With("tag", tag, "image", image)
	log.Info("testing version")

	if err := c.SetImage(ctx, cfg.Deployment, cfg.Container, image); err != nil {
		res.Status, res.Detail = StatusError, err.Error()
		return res
	}
	if err := c.WaitDeploymentReady(ctx, cfg.Deployment, cfg.ReadyTimeout); err != nil {
		res.Status, res.Detail = StatusNotReady, err.Error()
		return res
	}
	if err := c.ReplaceJob(ctx, job); err != nil {
		res.Status, res.Detail = StatusError, err.Error()
		return res
	}
	ok, err := c.WaitJob(ctx, cfg.JobName, cfg.JobTimeout)
	if err != nil {
		res.Status, res.Detail = StatusError, err.Error()
		return res
	}

	if smokeLogs, lerr := c.JobContainerLogs(ctx, cfg.JobName, "smoke"); lerr == nil {
		res.ReportedUA = reportedUA(smokeLogs)
	}
	if bullyLogs, lerr := c.JobContainerLogs(ctx, cfg.JobName, "chrome-bully"); lerr == nil {
		if remote, perr := capturedFramePath(bullyLogs); perr == nil {
			res.ChromeVersion = versionFromFrame(remote)
			local := filepath.Join(cfg.OutDir, "frames", tag+".png")
			if cerr := c.CopyFrame(ctx, collectorPodName, remote, local); cerr == nil {
				res.FramePath = local
			} else {
				log.Warn("frame copy failed", "err", cerr)
			}
		}
	}

	if ok {
		res.Status = StatusPass
	} else {
		res.Status, res.Detail = StatusFail, "smoke job failed"
	}
	return res
}

// versionFromFrame pulls the Chrome version out of a frame path like
// "/data/chrome-150.0.7871.114-20260101T000000.000Z.png".
func versionFromFrame(path string) string {
	base := filepath.Base(path)
	base = strings.TrimPrefix(base, "chrome-")
	if i := strings.LastIndex(base, "-"); i >= 0 {
		return base[:i]
	}
	return ""
}
```

- [ ] **Step 2: Finish `main.go` wiring**

Replace the body of `run` in `cmd/chrome-sweep/main.go` with:
```go
func run(kubeconfig string, cfg Config) error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	cs, err := kubeClient(kubeconfig)
	if err != nil {
		return err
	}
	cluster := NewCluster(cs, cfg.Namespace)

	ctx := context.Background()
	results, err := Run(ctx, cluster, cfg)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return err
	}
	md := renderMarkdown(results)
	if err := os.WriteFile(filepath.Join(cfg.OutDir, "report.md"), []byte(md), 0o644); err != nil {
		return err
	}
	js, err := renderJSON(results)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(cfg.OutDir, "report.json"), js, 0o644); err != nil {
		return err
	}
	fmt.Print(md)

	for _, r := range results {
		if r.Status != StatusPass {
			return fmt.Errorf("one or more versions did not pass; see %s/report.md", cfg.OutDir)
		}
	}
	return nil
}
```
Ensure `main.go` imports include `"context"`, `"fmt"`, `"log/slog"`, `"os"`, `"path/filepath"`. Remove the now-unused `time` import if the compiler flags it (it is still used by the flag defaults, so keep it).

- [ ] **Step 3: Build, vet, and run all unit tests**

Run:
```bash
go build -o ./var/chrome-sweep ./cmd/chrome-sweep && go vet ./cmd/chrome-sweep/ && go test ./cmd/chrome-sweep/
```
Expected: build succeeds, vet clean, all tests PASS.

- [ ] **Step 4: Real-cluster verification (integration)**

Prerequisites: `kubectl` context points at the cluster; namespace `ci` already has the Anubis deployment, the `anubis-key` secret, the `chrome` Deployment/Service, and the `chrome-bully-data` PVC (from `k8s/anubis` + `k8s/`). Pick two tags known to exist in `ghcr.io/techarohq/gubal/chrome`.

Run:
```bash
./var/chrome-sweep -out ./var/sweep 150 150
```
(Using the same tag twice is a safe smoke of the loop.)

Expected:
- Log lines `testing version` for each tag.
- Exit 0 and a printed Markdown table showing `2/2 passed`.
- `./var/sweep/report.md`, `./var/sweep/report.json`, and `./var/sweep/frames/150.png` exist.
- `kubectl get pod -n ci` shows no leftover `chrome-sweep-collector` pod (it is cleaned up).

Verify the frame is a real PNG:
```bash
file ./var/sweep/frames/150.png   # => PNG image data
```

If a version legitimately fails, confirm the tool records it and continues by running a bogus tag alongside a good one:
```bash
./var/chrome-sweep -out ./var/sweep-neg 150 does-not-exist ; echo "exit=$?"
```
Expected: `150` is `pass`, `does-not-exist` is `not-ready` (image never pulls), overall exit non-zero, and the collector is still cleaned up.

- [ ] **Step 5: Write the tool README**

Create `cmd/chrome-sweep/README.md`:
```markdown
# chrome-sweep

Tests a list of Chrome image tags one after another against the in-cluster
Anubis + httpdebug setup. For each tag it re-points the `chrome` Deployment,
waits for rollout, runs the `chrome-smoke` Job (`k8s/smoke-job.yaml`), and
records pass/fail plus the captured screenshot.

## Prerequisites

All in namespace `ci`: the Anubis deployment (`k8s/anubis`), the `anubis-key`
secret, the `chrome` Deployment/Service, and the `chrome-bully-data` PVC.

## Usage

    go build -o ./var/chrome-sweep ./cmd/chrome-sweep
    ./var/chrome-sweep -out ./var/sweep 110 120 130 150

Outputs `report.md`, `report.json`, and `frames/<tag>.png` under `-out`.
Exit code is non-zero if any version did not pass.

## Key flags

- `-namespace` (default `ci`), `-deployment` (`chrome`), `-container` (`chrome`)
- `-image-repo` (default `ghcr.io/techarohq/gubal/chrome`)
- `-job-manifest` (default `k8s/smoke-job.yaml`)
- `-ready-timeout` (default `3m`), `-job-timeout` (default `4m`)
- `-kubeconfig` (defaults to `$KUBECONFIG` or `~/.kube/config`)
```

- [ ] **Step 6: Commit**

```bash
git add cmd/chrome-sweep/sweep.go cmd/chrome-sweep/main.go cmd/chrome-sweep/README.md
git commit -m "feat(chrome-sweep): serial version sweep loop, reporting, and docs"
```

---

## Verification Summary

- Unit tests (`go test ./cmd/chrome-sweep/`) cover version parsing, log parsing, and report rendering.
- Fake-clientset tests cover image patching and job replacement.
- The Task 7 real-cluster run verifies rollout waiting, job running, log reading, frame copying, cleanup, the pass path, and the fail-and-continue path end to end.
- `go vet` and `go build -o ./var/...` are clean at each committing step.

## Notes for the implementer

- The sweep reuses `k8s/smoke-job.yaml` unchanged — that Job already drives Chrome through Anubis and asserts the header round-trip. Do not fork it.
- The frame filename encodes the reported Chrome version (`chrome-<version>-<ts>.png`), which `chrome-bully` logs; that is why we read the `captured` log line rather than guessing the name.
- `kubectl cp` requires `tar` in the source pod; the collector uses `busybox:1.36`, which has it.
- Because each version gets a fresh Chrome pod, the Anubis clearance cookie resets per version — every version genuinely re-solves the challenge.
- Client-go version `v0.31.x` is a known-good line; if Go 1.26 forces a newer minor, run `go mod tidy` and keep the API calls — they are stable across recent minors.
