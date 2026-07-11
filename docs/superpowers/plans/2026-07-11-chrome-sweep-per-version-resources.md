# Chrome Sweep Per-Version Resources Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Change `cmd/chrome-sweep` so each Chrome version gets its own set of non-PVC resources (Deployment, Service, NetworkPolicy, Job) named by version, created/torn down by the tool, and run in bounded parallel — sharing one PVC for frames.

**Architecture:** The tool loads the base k8s manifests (`k8s/deployment.yaml`, `service.yaml`, `networkpolicy.yaml`, `smoke-job.yaml`) as templates, deep-copies and retargets them per version to `chrome-<tag>` / `chrome-smoke-<tag>` with matching `app: chrome-<tag>` labels/selectors and the version's image + CDP host, creates them, waits for rollout, runs the smoke Job, collects UA + frame, then deletes that version's resources. A worker pool caps concurrency. The single `chrome-bully-data` PVC and one busybox collector pod stay shared.

**Tech Stack:** Go 1.26.4, `k8s.io/client-go` v0.31.1 (typed clients + fake clientset), `sigs.k8s.io/yaml`, `kubectl cp` (frame retrieval only). No new dependencies.

## Global Constraints

Copy these exact values; every task depends on them.

- Go module path: `github.com/TecharoHQ/gubal`; Go `go 1.26.4`.
- Build binaries into `./var`, never the repo root (e.g. `go build -o ./var/chrome-sweep ./cmd/chrome-sweep`).
- Kubernetes namespace: `ci`.
- Per version `<tag>`, all in `ci`, sharing one PVC:
  - Deployment `chrome-<tag>`, selector + pod labels `app: chrome-<tag>`, container `chrome` image `ghcr.io/techarohq/gubal/chrome:<tag>`.
  - Service `chrome-<tag>`, selector `app: chrome-<tag>`, port 9222.
  - NetworkPolicy `chrome-<tag>-lockdown`, podSelector `app: chrome-<tag>`.
  - Job `chrome-smoke-<tag>`, with the CDP host rewritten from `chrome:9222` to `chrome-<tag>:9222` in every container arg.
- Base (template) names/labels: name `chrome`, label `app: chrome`, service host `chrome`. The base manifests keep their current single-version form.
- Shared / not templated: PVC `chrome-bully-data` (RWX) mounted at `/data`; Anubis + `anubis-key` secret; one `chrome-sweep-collector` busybox pod.
- `chrome-bully` logs the frame as `{"level":"INFO","msg":"captured","path":"/data/chrome-<version>-<ts>.png"}`; the `smoke` container logs `User-Agent: <ua>` and keeps a `Host: localhost:9222` header that MUST NOT be rewritten.
- Default parallelism: `8`.
- Prerequisites that MUST already exist in `ci`: the Anubis deployment, the `anubis-key` secret, and the `chrome-bully-data` PVC. The tool creates the per-version chrome resources itself.
- Commit style: Conventional Commits (`feat(...)`, `refactor(...)`, `test(...)`, etc.).

---

## File Structure

- `cmd/chrome-sweep/retarget.go` — NEW. Pure per-version mutation functions (`versionedName`, `retargetDeployment`, `retargetService`, `retargetNetworkPolicy`, `retargetJob`) plus manifest loaders (`loadDeployment`, `loadService`, `loadNetworkPolicy`).
- `cmd/chrome-sweep/retarget_test.go` — NEW. Table tests for the mutation functions + a loader smoke test against the real `k8s/*.yaml`.
- `cmd/chrome-sweep/cluster.go` — MODIFY. Add `waitGone` helper; add `CreateOrReplaceDeployment`/`CreateOrReplaceService`/`CreateOrReplaceNetworkPolicy` and `DeleteVersionResources`; refactor `ReplaceJob`'s clear-wait onto `waitGone`; remove the now-dead `SetImage`.
- `cmd/chrome-sweep/cluster_test.go` — MODIFY. Remove `TestSetImage`; add fake-clientset tests for create-or-replace and delete.
- `cmd/chrome-sweep/sweep.go` — REWRITE. Per-version templating + bounded-parallel worker pool + teardown.
- `cmd/chrome-sweep/main.go` — MODIFY. New Config fields + flags; updated package doc.
- `cmd/chrome-sweep/README.md` — MODIFY. Document the per-version model, new flags, updated prerequisites.

The pure mutation functions and the fake-clientset CRUD are the testable core; the bounded-parallel orchestration and real frame copy are covered by the documented real-cluster run in the final task.

---

### Task 1: Per-version mutation functions and manifest loaders

**Files:**
- Create: `cmd/chrome-sweep/retarget.go`
- Test: `cmd/chrome-sweep/retarget_test.go`

**Interfaces:**
- Consumes: nothing (pure helpers + file loaders).
- Produces:
  - `func versionedName(base, tag string) string`
  - `func retargetDeployment(dep *appsv1.Deployment, name, container, image string)`
  - `func retargetService(svc *corev1.Service, name string)`
  - `func retargetNetworkPolicy(np *networkingv1.NetworkPolicy, name string)`
  - `func retargetJob(job *batchv1.Job, name, baseHost, versionedHost string)`
  - `func loadDeployment(path, namespace string) (*appsv1.Deployment, error)`
  - `func loadService(path, namespace string) (*corev1.Service, error)`
  - `func loadNetworkPolicy(path, namespace string) (*networkingv1.NetworkPolicy, error)`

- [ ] **Step 1: Write the failing tests**

Create `cmd/chrome-sweep/retarget_test.go`:
```go
package main

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestVersionedName(t *testing.T) {
	if got := versionedName("chrome", "150"); got != "chrome-150" {
		t.Fatalf("got %q, want chrome-150", got)
	}
	if got := versionedName("chrome-smoke", "150"); got != "chrome-smoke-150" {
		t.Fatalf("got %q, want chrome-smoke-150", got)
	}
}

func TestRetargetDeployment(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "chrome", Labels: map[string]string{"app": "chrome"}},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "chrome"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "chrome"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{
					{Name: "chrome", Image: "chrome"},
					{Name: "sidecar", Image: "sidecar:1"},
				}},
			},
		},
	}
	retargetDeployment(dep, "chrome-150", "chrome", "repo/chrome:150")
	if dep.Name != "chrome-150" {
		t.Fatalf("name = %q", dep.Name)
	}
	if dep.Spec.Selector.MatchLabels["app"] != "chrome-150" {
		t.Fatalf("selector app = %q", dep.Spec.Selector.MatchLabels["app"])
	}
	if dep.Spec.Template.Labels["app"] != "chrome-150" {
		t.Fatalf("template app = %q", dep.Spec.Template.Labels["app"])
	}
	if dep.Spec.Template.Spec.Containers[0].Image != "repo/chrome:150" {
		t.Fatalf("chrome image = %q", dep.Spec.Template.Spec.Containers[0].Image)
	}
	if dep.Spec.Template.Spec.Containers[1].Image != "sidecar:1" {
		t.Fatalf("sidecar image should be untouched, got %q", dep.Spec.Template.Spec.Containers[1].Image)
	}
}

func TestRetargetService(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "chrome"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "chrome"}},
	}
	retargetService(svc, "chrome-150")
	if svc.Name != "chrome-150" || svc.Spec.Selector["app"] != "chrome-150" {
		t.Fatalf("svc = %q / %q", svc.Name, svc.Spec.Selector["app"])
	}
}

func TestRetargetNetworkPolicy(t *testing.T) {
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "chrome-lockdown"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "chrome"}},
		},
	}
	retargetNetworkPolicy(np, "chrome-150")
	if np.Name != "chrome-150-lockdown" {
		t.Fatalf("np name = %q", np.Name)
	}
	if np.Spec.PodSelector.MatchLabels["app"] != "chrome-150" {
		t.Fatalf("np podSelector app = %q", np.Spec.PodSelector.MatchLabels["app"])
	}
}

func TestRetargetJob(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "chrome-smoke"},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "smoke",
					Args: []string{
						"echo waiting for chrome:9222; curl -H 'Host: localhost:9222' http://chrome:9222/json/version",
					},
				},
				{
					Name: "chrome-bully",
					Args: []string{"-cdp-url=http://chrome:9222", "-target-url=https://example"},
				},
			},
		}}},
	}
	retargetJob(job, "chrome-smoke-150", "chrome", "chrome-150")
	if job.Name != "chrome-smoke-150" {
		t.Fatalf("job name = %q", job.Name)
	}
	smoke := job.Spec.Template.Spec.Containers[0].Args[0]
	if want := "http://chrome-150:9222/json/version"; !strings.Contains(smoke, want) {
		t.Fatalf("smoke arg not retargeted: %q", smoke)
	}
	if !strings.Contains(smoke, "Host: localhost:9222") {
		t.Fatalf("localhost host header must be preserved: %q", smoke)
	}
	if got := job.Spec.Template.Spec.Containers[1].Args[0]; got != "-cdp-url=http://chrome-150:9222" {
		t.Fatalf("chrome-bully cdp arg = %q", got)
	}
}

func TestLoadManifests(t *testing.T) {
	dep, err := loadDeployment("../../k8s/deployment.yaml", "ci")
	if err != nil || dep.Name != "chrome" || dep.Namespace != "ci" {
		t.Fatalf("loadDeployment: %v name=%q ns=%q", err, dep.GetName(), dep.GetNamespace())
	}
	svc, err := loadService("../../k8s/service.yaml", "ci")
	if err != nil || svc.Name != "chrome" {
		t.Fatalf("loadService: %v name=%q", err, svc.GetName())
	}
	np, err := loadNetworkPolicy("../../k8s/networkpolicy.yaml", "ci")
	if err != nil || np.Name != "chrome-lockdown" {
		t.Fatalf("loadNetworkPolicy: %v name=%q", err, np.GetName())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/chrome-sweep/ -run 'TestVersionedName|TestRetarget|TestLoadManifests' -v`
Expected: FAIL — `undefined: versionedName` etc.

- [ ] **Step 3: Write the implementation**

Create `cmd/chrome-sweep/retarget.go`:
```go
package main

import (
	"fmt"
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/yaml"
)

// versionedName joins a base resource name and a version tag: chrome + 150 -> chrome-150.
func versionedName(base, tag string) string {
	return base + "-" + tag
}

// retargetDeployment renames the Deployment and repoints its selector/pod labels
// to name, and sets the named container's image.
func retargetDeployment(dep *appsv1.Deployment, name, container, image string) {
	dep.Name = name
	if dep.Labels == nil {
		dep.Labels = map[string]string{}
	}
	dep.Labels["app"] = name
	if dep.Spec.Selector == nil {
		dep.Spec.Selector = &metav1.LabelSelector{}
	}
	if dep.Spec.Selector.MatchLabels == nil {
		dep.Spec.Selector.MatchLabels = map[string]string{}
	}
	dep.Spec.Selector.MatchLabels["app"] = name
	if dep.Spec.Template.Labels == nil {
		dep.Spec.Template.Labels = map[string]string{}
	}
	dep.Spec.Template.Labels["app"] = name
	for i := range dep.Spec.Template.Spec.Containers {
		if dep.Spec.Template.Spec.Containers[i].Name == container {
			dep.Spec.Template.Spec.Containers[i].Image = image
		}
	}
}

// retargetService renames the Service and repoints its selector to name.
func retargetService(svc *corev1.Service, name string) {
	svc.Name = name
	if svc.Labels == nil {
		svc.Labels = map[string]string{}
	}
	svc.Labels["app"] = name
	if svc.Spec.Selector == nil {
		svc.Spec.Selector = map[string]string{}
	}
	svc.Spec.Selector["app"] = name
}

// retargetNetworkPolicy renames the policy to <name>-lockdown and repoints its
// podSelector to name.
func retargetNetworkPolicy(np *networkingv1.NetworkPolicy, name string) {
	np.Name = name + "-lockdown"
	if np.Spec.PodSelector.MatchLabels == nil {
		np.Spec.PodSelector.MatchLabels = map[string]string{}
	}
	np.Spec.PodSelector.MatchLabels["app"] = name
}

// retargetJob renames the Job and rewrites the CDP host in every container arg
// from baseHost:9222 to versionedHost:9222 (leaving Host: localhost:9222 alone).
func retargetJob(job *batchv1.Job, name, baseHost, versionedHost string) {
	job.Name = name
	old := baseHost + ":9222"
	replacement := versionedHost + ":9222"
	cs := job.Spec.Template.Spec.Containers
	for i := range cs {
		for j := range cs[i].Args {
			cs[i].Args[j] = strings.ReplaceAll(cs[i].Args[j], old, replacement)
		}
	}
}

func loadDeployment(path, namespace string) (*appsv1.Deployment, error) {
	var d appsv1.Deployment
	if err := decodeManifest(path, &d); err != nil {
		return nil, err
	}
	d.Namespace = namespace
	return &d, nil
}

func loadService(path, namespace string) (*corev1.Service, error) {
	var s corev1.Service
	if err := decodeManifest(path, &s); err != nil {
		return nil, err
	}
	s.Namespace = namespace
	return &s, nil
}

func loadNetworkPolicy(path, namespace string) (*networkingv1.NetworkPolicy, error) {
	var np networkingv1.NetworkPolicy
	if err := decodeManifest(path, &np); err != nil {
		return nil, err
	}
	np.Namespace = namespace
	return &np, nil
}

func decodeManifest(path string, into any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("decoding %s: %w", path, err)
	}
	return nil
}
```

NOTE: replace the `&metav1LabelSelector` placeholder with a proper zero value. Use this exact body for the nil-selector guard instead of the placeholder above:
```go
	if dep.Spec.Selector == nil {
		dep.Spec.Selector = &metav1.LabelSelector{}
	}
```
and add `metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"` to the imports. (The base `k8s/deployment.yaml` always has a selector, so this guard is defensive; keep it correct.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/chrome-sweep/ -run 'TestVersionedName|TestRetarget|TestLoadManifests' -v`
Expected: PASS. Then `go vet ./cmd/chrome-sweep/` — clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/chrome-sweep/retarget.go cmd/chrome-sweep/retarget_test.go
git commit -m "feat(chrome-sweep): per-version manifest retargeting and loaders"
```

---

### Task 2: Cluster CRUD for per-version resources

**Files:**
- Modify: `cmd/chrome-sweep/cluster.go`
- Modify: `cmd/chrome-sweep/cluster_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `func waitGone(ctx context.Context, timeout time.Duration, get func(context.Context) error) error`
  - `func (c *Cluster) CreateOrReplaceDeployment(ctx context.Context, dep *appsv1.Deployment, timeout time.Duration) error`
  - `func (c *Cluster) CreateOrReplaceService(ctx context.Context, svc *corev1.Service, timeout time.Duration) error`
  - `func (c *Cluster) CreateOrReplaceNetworkPolicy(ctx context.Context, np *networkingv1.NetworkPolicy, timeout time.Duration) error`
  - `func (c *Cluster) DeleteVersionResources(ctx context.Context, name, jobName string) error`
- Removes: `SetImage` (and `TestSetImage`).

- [ ] **Step 1: Update the tests (remove TestSetImage, add new fake-clientset tests)**

In `cmd/chrome-sweep/cluster_test.go`, DELETE the entire `TestSetImage` function (lines defining `func TestSetImage(...)`). Add these imports if missing: `appsv1 "k8s.io/api/apps/v1"` (already present), `networkingv1 "k8s.io/api/networking/v1"`. Then add:
```go
func TestCreateOrReplaceDeploymentCreatesWhenAbsent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	c := NewCluster(cs, "ci")
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "chrome-150"}}
	if err := c.CreateOrReplaceDeployment(context.Background(), dep, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.AppsV1().Deployments("ci").Get(context.Background(), "chrome-150", metav1.GetOptions{}); err != nil {
		t.Fatalf("deployment not created: %v", err)
	}
}

func TestCreateOrReplaceServiceReplacesExisting(t *testing.T) {
	existing := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "chrome-150", Namespace: "ci"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "old"}},
	}
	cs := fake.NewSimpleClientset(existing)
	c := NewCluster(cs, "ci")
	updated := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "chrome-150"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "chrome-150"}},
	}
	if err := c.CreateOrReplaceService(context.Background(), updated, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := cs.CoreV1().Services("ci").Get(context.Background(), "chrome-150", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Selector["app"] != "chrome-150" {
		t.Fatalf("selector app = %q, want chrome-150", got.Spec.Selector["app"])
	}
}

func TestDeleteVersionResourcesRemovesSet(t *testing.T) {
	objs := []runtimeObject{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "chrome-150", Namespace: "ci"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "chrome-150", Namespace: "ci"}},
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "chrome-150-lockdown", Namespace: "ci"}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "chrome-smoke-150", Namespace: "ci"}},
	}
	cs := fake.NewSimpleClientset(objs...)
	c := NewCluster(cs, "ci")
	if err := c.DeleteVersionResources(context.Background(), "chrome-150", "chrome-smoke-150"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := cs.AppsV1().Deployments("ci").Get(context.Background(), "chrome-150", metav1.GetOptions{}); err == nil {
		t.Fatal("deployment still present")
	}
	if _, err := cs.NetworkingV1().NetworkPolicies("ci").Get(context.Background(), "chrome-150-lockdown", metav1.GetOptions{}); err == nil {
		t.Fatal("networkpolicy still present")
	}
	// Calling again with everything absent must be a no-op (tolerate NotFound).
	if err := c.DeleteVersionResources(context.Background(), "chrome-150", "chrome-smoke-150"); err != nil {
		t.Fatalf("second delete should tolerate NotFound: %v", err)
	}
}
```
The `runtimeObject` alias keeps `fake.NewSimpleClientset` readable; add this at the top of the test file's declarations:
```go
type runtimeObject = runtime.Object
```
and add `"k8s.io/apimachinery/pkg/runtime"` and `"time"` to the test imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/chrome-sweep/ -run 'TestCreateOrReplace|TestDeleteVersionResources' -v`
Expected: FAIL — `undefined: CreateOrReplaceDeployment` etc. (compile error).

- [ ] **Step 3: Write the implementation**

In `cmd/chrome-sweep/cluster.go`:

3a. Add `"errors"` to the imports and `appsv1 "k8s.io/api/apps/v1"`, `networkingv1 "k8s.io/api/networking/v1"` (keep existing imports).

3b. DELETE the `SetImage` method (the whole `func (c *Cluster) SetImage(...) {...}` block) and remove the now-unused `"k8s.io/apimachinery/pkg/types"` import.

3c. Add the `waitGone` helper (place it just above `ReplaceJob`):
```go
// waitGone polls until get reports the object is NotFound.
func waitGone(ctx context.Context, timeout time.Duration, get func(context.Context) error) error {
	return wait.PollUntilContextTimeout(ctx, time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			err := get(ctx)
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			return false, nil
		})
}
```

3d. Refactor `ReplaceJob`'s inline clear-wait to use `waitGone`. Replace the `if err == nil { if werr := wait.PollUntilContextTimeout(...) {...} }` block with:
```go
	if err == nil {
		if werr := waitGone(ctx, 2*time.Minute, func(ctx context.Context) error {
			_, e := c.cs.BatchV1().Jobs(c.ns).Get(ctx, job.Name, metav1.GetOptions{})
			return e
		}); werr != nil {
			return fmt.Errorf("waiting for old job to clear: %w", werr)
		}
	}
```

3e. Add the three create-or-replace methods and the delete method (place after `ReplaceJob`):
```go
// CreateOrReplaceDeployment deletes any existing Deployment of the same name
// (waiting for it to clear) and creates the given one. Delete+create rather than
// update because a Deployment's selector is immutable.
func (c *Cluster) CreateOrReplaceDeployment(ctx context.Context, dep *appsv1.Deployment, timeout time.Duration) error {
	dep.Namespace = c.ns
	api := c.cs.AppsV1().Deployments(c.ns)
	err := api.Delete(ctx, dep.Name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting old deployment %s: %w", dep.Name, err)
	}
	if err == nil {
		if werr := waitGone(ctx, timeout, func(ctx context.Context) error {
			_, e := api.Get(ctx, dep.Name, metav1.GetOptions{})
			return e
		}); werr != nil {
			return fmt.Errorf("waiting for old deployment %s to clear: %w", dep.Name, werr)
		}
	}
	if _, err := api.Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating deployment %s: %w", dep.Name, err)
	}
	return nil
}

// CreateOrReplaceService deletes any existing Service of the same name and creates
// the given one.
func (c *Cluster) CreateOrReplaceService(ctx context.Context, svc *corev1.Service, timeout time.Duration) error {
	svc.Namespace = c.ns
	api := c.cs.CoreV1().Services(c.ns)
	err := api.Delete(ctx, svc.Name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting old service %s: %w", svc.Name, err)
	}
	if err == nil {
		if werr := waitGone(ctx, timeout, func(ctx context.Context) error {
			_, e := api.Get(ctx, svc.Name, metav1.GetOptions{})
			return e
		}); werr != nil {
			return fmt.Errorf("waiting for old service %s to clear: %w", svc.Name, werr)
		}
	}
	if _, err := api.Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating service %s: %w", svc.Name, err)
	}
	return nil
}

// CreateOrReplaceNetworkPolicy deletes any existing NetworkPolicy of the same name
// and creates the given one.
func (c *Cluster) CreateOrReplaceNetworkPolicy(ctx context.Context, np *networkingv1.NetworkPolicy, timeout time.Duration) error {
	np.Namespace = c.ns
	api := c.cs.NetworkingV1().NetworkPolicies(c.ns)
	err := api.Delete(ctx, np.Name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting old networkpolicy %s: %w", np.Name, err)
	}
	if err == nil {
		if werr := waitGone(ctx, timeout, func(ctx context.Context) error {
			_, e := api.Get(ctx, np.Name, metav1.GetOptions{})
			return e
		}); werr != nil {
			return fmt.Errorf("waiting for old networkpolicy %s to clear: %w", np.Name, werr)
		}
	}
	if _, err := api.Create(ctx, np, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating networkpolicy %s: %w", np.Name, err)
	}
	return nil
}

// DeleteVersionResources removes a version's Deployment, Service, NetworkPolicy
// (<name>-lockdown), and Job (jobName). It tolerates already-absent resources and
// returns the joined error of any real failures (best-effort teardown).
func (c *Cluster) DeleteVersionResources(ctx context.Context, name, jobName string) error {
	fg := metav1.DeletePropagationForeground
	var errs []error
	if err := c.cs.AppsV1().Deployments(c.ns).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &fg}); err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, err)
	}
	if err := c.cs.CoreV1().Services(c.ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, err)
	}
	if err := c.cs.NetworkingV1().NetworkPolicies(c.ns).Delete(ctx, name+"-lockdown", metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, err)
	}
	if err := c.cs.BatchV1().Jobs(c.ns).Delete(ctx, jobName, metav1.DeleteOptions{PropagationPolicy: &fg}); err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
go test ./cmd/chrome-sweep/ -run 'TestCreateOrReplace|TestDeleteVersionResources|TestReplaceJob' -v && go vet ./cmd/chrome-sweep/
```
Expected: PASS, vet clean. (`go build -o ./var/chrome-sweep ./cmd/chrome-sweep` will still FAIL here because `sweep.go` references `SetImage` — that is fixed in Task 3. Do NOT try to build the whole package in this task; the package-scoped `go test` above compiles the test binary, which also still references `SetImage` via `sweep.go`. To keep this task self-contained, in this same step also delete the two now-dead references in `sweep.go`: none exist yet besides `SetImage`; if the test compile fails on `sweep.go`'s `SetImage` call, that is expected and resolved in Task 3. If you need a green `go test` here, temporarily comment out the `c.SetImage(...)` call in `sweep.go` and its `if err := ...` block — Task 3 rewrites that file wholesale.)

NOTE for the implementer: the cleanest path is to do Task 2 and Task 3 back-to-back; the package will not compile between them because `sweep.go` still calls the removed `SetImage`. If your workflow requires a compiling tree at each commit, fold Task 2 and Task 3 into one commit. Otherwise, commit Task 2 with the test-only verification above (the new tests compile and pass in isolation via `-run`), then immediately proceed to Task 3.

- [ ] **Step 5: Commit**

```bash
git add cmd/chrome-sweep/cluster.go cmd/chrome-sweep/cluster_test.go
git commit -m "refactor(chrome-sweep): per-version resource CRUD, drop in-place SetImage"
```

---

### Task 3: Bounded-parallel per-version sweep loop + flags

**Files:**
- Rewrite: `cmd/chrome-sweep/sweep.go`
- Modify: `cmd/chrome-sweep/main.go`

**Interfaces:**
- Consumes: everything from Tasks 1-2, plus existing `WaitDeploymentReady`, `ReplaceJob`, `WaitJob`, `JobContainerLogs`, `EnsureCollector`, `DeleteCollector`, `CopyFrame`, `reportedUA`, `capturedFramePath`, `versionFromFrame`, `renderMarkdown`, `renderJSON`, `Result`, `Status`.
- Produces: `func Run(ctx context.Context, c *Cluster, cfg Config) ([]Result, error)` (same signature, new body). New Config fields: `DeploymentManifest`, `ServiceManifest`, `NetworkPolicyManifest string`, `Parallelism int`.

- [ ] **Step 1: Add the new Config fields and flags in main.go**

In `cmd/chrome-sweep/main.go`, add to the `Config` struct (after `JobManifest`/`JobName`):
```go
	DeploymentManifest    string
	ServiceManifest       string
	NetworkPolicyManifest string
	Parallelism           int
```
Add these flags in `main()` (after the existing `-job-name` flag):
```go
	flag.StringVar(&cfg.DeploymentManifest, "deployment-manifest", "k8s/deployment.yaml", "base Deployment manifest to template per version")
	flag.StringVar(&cfg.ServiceManifest, "service-manifest", "k8s/service.yaml", "base Service manifest to template per version")
	flag.StringVar(&cfg.NetworkPolicyManifest, "networkpolicy-manifest", "k8s/networkpolicy.yaml", "base NetworkPolicy manifest to template per version")
	flag.IntVar(&cfg.Parallelism, "parallelism", 8, "max number of versions tested concurrently")
```
Update the usage text of the existing `-deployment` and `-job-name` flags:
```go
	flag.StringVar(&cfg.Deployment, "deployment", "chrome", "base name for per-version chrome resources (Deployment/Service/NetworkPolicy)")
	flag.StringVar(&cfg.JobName, "job-name", "chrome-smoke", "base name for per-version smoke Jobs; per version appends -<tag>")
```
Update the package doc comment at the top of `main.go` to:
```go
// Command chrome-sweep tests a list of Chrome image tags in bounded parallel: for
// each tag it creates a per-version chrome Deployment/Service/NetworkPolicy and
// smoke Job (all named chrome-<tag>), waits for rollout, runs the smoke Job,
// records a pass/fail + screenshot, then tears the version's resources down. One
// shared PVC collects every version's frames.
```

- [ ] **Step 2: Rewrite sweep.go**

Replace the ENTIRE contents of `cmd/chrome-sweep/sweep.go` with:
```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

const collectorPodName = "chrome-sweep-collector"

// baseManifests holds the decoded template objects, loaded once and deep-copied
// per version before retargeting.
type baseManifests struct {
	deployment *appsv1.Deployment
	service    *corev1.Service
	netpol     *networkingv1.NetworkPolicy
	job        *batchv1.Job
}

func loadBaseManifests(cfg Config) (baseManifests, error) {
	dep, err := loadDeployment(cfg.DeploymentManifest, cfg.Namespace)
	if err != nil {
		return baseManifests{}, err
	}
	svc, err := loadService(cfg.ServiceManifest, cfg.Namespace)
	if err != nil {
		return baseManifests{}, err
	}
	np, err := loadNetworkPolicy(cfg.NetworkPolicyManifest, cfg.Namespace)
	if err != nil {
		return baseManifests{}, err
	}
	job, err := loadJob(cfg.JobManifest, cfg.Namespace)
	if err != nil {
		return baseManifests{}, err
	}
	return baseManifests{deployment: dep, service: svc, netpol: np, job: job}, nil
}

// Run tests each version in cfg.Versions in bounded parallel (cfg.Parallelism at
// once) and returns one Result each, in argument order. A failure on one version
// is recorded and does not stop the others.
func Run(ctx context.Context, c *Cluster, cfg Config) ([]Result, error) {
	base, err := loadBaseManifests(cfg)
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

	parallelism := cfg.Parallelism
	if parallelism < 1 {
		parallelism = 1
	}
	results := make([]Result, len(cfg.Versions))
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	for i, tag := range cfg.Versions {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, tag string) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = sweepOne(ctx, c, cfg, base, tag)
		}(i, tag)
	}
	wg.Wait()
	return results, nil
}

func sweepOne(ctx context.Context, c *Cluster, cfg Config, base baseManifests, tag string) Result {
	name := versionedName(cfg.Deployment, tag) // chrome-<tag>
	jobName := versionedName(cfg.JobName, tag)  // chrome-smoke-<tag>
	image := fmt.Sprintf("%s:%s", cfg.ImageRepo, tag)
	res := Result{Tag: tag}
	log := slog.With("tag", tag, "image", image, "name", name)
	log.Info("testing version")

	// Tear this version's resources down when done, even on early return. Uses a
	// fresh context so cleanup runs even if ctx was cancelled.
	defer func() {
		if derr := c.DeleteVersionResources(context.Background(), name, jobName); derr != nil {
			log.Warn("teardown failed", "err", derr)
		}
	}()

	dep := base.deployment.DeepCopy()
	retargetDeployment(dep, name, cfg.Container, image)
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
	retargetJob(job, jobName, cfg.Deployment, name)
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
			res.ChromeVersion = versionFromFrame(remote)
			local := filepath.Join(cfg.OutDir, "frames", tag+".png")
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

- [ ] **Step 3: Build, vet, and run all unit tests**

Run:
```bash
go build -o ./var/chrome-sweep ./cmd/chrome-sweep && go vet ./cmd/chrome-sweep/ && go test ./cmd/chrome-sweep/
```
Expected: build succeeds, vet clean, all tests PASS.

- [ ] **Step 4: Confirm the no-cluster path still works**

Run: `./var/chrome-sweep 2>&1 | head -1`
Expected: a fatal JSON log containing `no versions given` (this path needs no cluster). Also confirm `./var/chrome-sweep -h 2>&1 | grep parallelism` shows the new flag.

- [ ] **Step 5: Commit**

```bash
git add cmd/chrome-sweep/sweep.go cmd/chrome-sweep/main.go
git commit -m "feat(chrome-sweep): bounded-parallel per-version resource sweep"
```

---

### Task 4: README, real-cluster verification, and docs

**Files:**
- Modify: `cmd/chrome-sweep/README.md`

**Interfaces:**
- Consumes everything above. Produces no new code.

- [ ] **Step 1: Update the tool README**

Replace the contents of `cmd/chrome-sweep/README.md` with:
```markdown
# chrome-sweep

Tests a list of Chrome image tags in bounded parallel against the in-cluster
Anubis + httpdebug setup. For each tag it creates a per-version chrome
Deployment/Service/NetworkPolicy and a `chrome-smoke-<tag>` Job (all labelled
`app: chrome-<tag>`, all in namespace `ci`), waits for rollout, runs the smoke
Job (`k8s/smoke-job.yaml`), records pass/fail plus the captured screenshot, then
tears that version's resources down. One shared PVC collects every version's
frames.

## Prerequisites

All in namespace `ci`: the Anubis deployment (`k8s/anubis`), the `anubis-key`
secret, and the `chrome-bully-data` PVC. The per-version chrome resources are
created by the tool — they must NOT pre-exist under the same names.

## Usage

    go build -o ./var/chrome-sweep ./cmd/chrome-sweep
    ./var/chrome-sweep -out ./var/sweep 110 120 130 150

Outputs `report.md`, `report.json`, and `frames/<tag>.png` under `-out`.
Exit code is non-zero if any version did not pass.

## Key flags

- `-parallelism` (default `8`) — max versions tested at once
- `-namespace` (default `ci`), `-deployment` (base name `chrome`), `-container` (`chrome`)
- `-image-repo` (default `ghcr.io/techarohq/gubal/chrome`)
- `-deployment-manifest` (`k8s/deployment.yaml`), `-service-manifest` (`k8s/service.yaml`), `-networkpolicy-manifest` (`k8s/networkpolicy.yaml`), `-job-manifest` (`k8s/smoke-job.yaml`)
- `-ready-timeout` (default `3m`), `-job-timeout` (default `4m`)
- `-kubeconfig` (defaults to `$KUBECONFIG` or `~/.kube/config`)
```

- [ ] **Step 2: Commit the README**

```bash
git add cmd/chrome-sweep/README.md
git commit -m "docs(chrome-sweep): document per-version resource model and flags"
```

- [ ] **Step 3: Real-cluster verification (integration — run by a human with a live cluster)**

Prerequisites: `kubectl` context points at the cluster; namespace `ci` has Anubis, the `anubis-key` secret, and the `chrome-bully-data` PVC. There must be NO pre-existing `chrome-<tag>` Deployment/Service/NetworkPolicy for the tags under test. Pick two tags known to exist in `ghcr.io/techarohq/gubal/chrome`.

Run:
```bash
./var/chrome-sweep -out ./var/sweep -parallelism 2 150 150
```
Expected:
- Log lines `testing version` for each tag.
- Two `chrome-150` resource sets are created (names collide when the same tag is passed twice — use two DISTINCT real tags, e.g. `120 150`, for a true parallel smoke). Prefer:
```bash
./var/chrome-sweep -out ./var/sweep -parallelism 2 120 150
```
- Exit 0 and a printed Markdown table showing `2/2 passed`.
- `./var/sweep/report.md`, `./var/sweep/report.json`, `./var/sweep/frames/120.png`, `./var/sweep/frames/150.png` exist and are PNGs (`file ./var/sweep/frames/150.png` => PNG image data).
- After the run, `kubectl get deploy,svc,netpol,job -n ci` shows NO leftover `chrome-120`/`chrome-150`/`chrome-smoke-120`/`chrome-smoke-150` (torn down), and no `chrome-sweep-collector` pod.

Fail-and-continue check:
```bash
./var/chrome-sweep -out ./var/sweep-neg -parallelism 2 150 does-not-exist ; echo "exit=$?"
```
Expected: `150` is `pass`, `does-not-exist` is `not-ready` (image never pulls), overall exit non-zero, and both versions' resources plus the collector are cleaned up.

---

## Verification Summary

- Unit tests (`go test ./cmd/chrome-sweep/`) cover the retarget mutation functions (name/label/selector/image/CDP-host rewrites, with the `Host: localhost:9222` header preserved), the manifest loaders, and the fake-clientset create-or-replace + delete methods.
- `go vet` and `go build -o ./var/...` are clean at each committing step (except the intentional Task 2→3 transient noted in Task 2 Step 4).
- The Task 4 real-cluster run verifies per-version create, rollout wait, parallel job runs, log reading, frame copying off the shared PVC, teardown, the pass path, and the fail-and-continue path end to end.

## Notes for the implementer

- Deep-copy every base manifest before retargeting (`base.deployment.DeepCopy()` etc.) — parallel workers share the loaded base objects, and retargeting mutates in place. This is done in `sweepOne`; do not remove it.
- The per-version NetworkPolicy is load-bearing security, not cosmetic: without a policy selecting `app: chrome-<tag>`, that pod's unauthenticated CDP port would be open namespace-wide. Always create it before waiting for the Deployment.
- `retargetJob` rewrites `chrome:9222` → `chrome-<tag>:9222` across all container args, which covers both the `smoke` container's curl script and the `chrome-bully` `-cdp-url` flag while leaving the `Host: localhost:9222` header untouched (that host is `localhost`, not `chrome`).
- Tasks 2 and 3 together make the tree compile; the package does not build in between because `sweep.go` still references the removed `SetImage` until Task 3 rewrites it. Commit them back-to-back.
- `DeleteVersionResources` and the deferred teardown are best-effort: they log a warning on failure and never fail the run — frames are already copied to `./var` by the time teardown runs.
- client-go `v0.31.x` typed clients (`AppsV1`, `CoreV1`, `NetworkingV1`, `BatchV1`) and the fake clientset are already in `go.mod`; no `go get` is needed.
```
