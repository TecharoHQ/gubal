# Anubis Policy Matrix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Sweep every browser/version against *multiple* Anubis bot-policy rulesets (a compiled-in folder of `.yaml` files), and report which policy rulesets passed and failed — each ruleset named by its filename without extension.

**Architecture:** Today Anubis is a shared singleton that runs its built-in default policy; a sweep tests all browsers against that one config. This plan adds a folder of Anubis policy files embedded into the `chromesweep` package via `go:embed`. `Run` gains an **outer, sequential loop over policies**: for each policy it renders the ruleset into a per-policy ConfigMap, patches the live Anubis Deployment to mount that ConfigMap and set `POLICY_FNAME`, waits for the rollout, then runs the existing (parallel) browser/version matrix against it — tagging every `Result` with the policy name. At the end the original Anubis pod template is restored from a snapshot. The report gains a per-policy pass/fail summary table and groups results by policy.

**Tech Stack:** Go 1.26.4, `client-go` (typed clientset + `fake` clientset for tests), `embed`, `sigs.k8s.io/yaml`, Anubis `botPolicies.yaml` config.

## Global Constraints

- **Go module:** `github.com/TecharoHQ/gubal`, Go 1.26.4.
- **Binaries build into `./var`**, never the repo root: `go build -o ./var/chrome-sweep ./cmd/chrome-sweep`.
- **`chromesweep` reads `k8s/*.yaml` manifests from disk at runtime** (relative to CWD). Policy files, by contrast, are **compiled into the binary** via `go:embed` — that is the explicit ask here.
- **The per-version NetworkPolicy is load-bearing security** — unchanged by this plan; do not remove it.
- **Anubis is a shared singleton** (`replicas: 1`, one Deployment/Service named `anubis` in namespace `ci`). Only one policy can be live at a time, so the **policy loop must be sequential**; browser/version parallelism stays *inside* each policy.
- **Use `log/slog`** (never `log`). CLIs use `flagenv.Parse()` then `flag.Parse()`.
- **Tests:** pure functions and client-go ops are unit-tested (client-go with the `fake` clientset). The parallel/real-cluster orchestration in `Run` is only exercised by a live cluster (integration Task 8).
- **Anubis policy file schema** (`ghcr.io/techarohq/anubis:latest`): top-level keys include `bots`, `dnsbl`, `status_codes`, `openGraph`, `store`, `thresholds`. Only `bots` is required for a minimal file. Each bot rule has `name`, a matcher (`user_agent_regex`, `path_regex`, `expression`, `geoip`, …), and `action` ∈ {`ALLOW`, `WEIGH`, `CHALLENGE`, `DENY`}. A rule may carry a `challenge:` block with `algorithm` (`metarefresh`/`fast`/`slow`) and `difficulty` (int).
- **Existing smoke-job assertion constraint:** `k8s/smoke-job.yaml` / `k8s/firefox/smoke-job.yaml` have a `smoke` container that curls Anubis with `-H "User-Agent: Mozilla"`, requires HTTP 200, **and `grep -qi anubis` on the body**. An Anubis *challenge* interstitial contains "Anubis" (passes the grep); an `ALLOW`ed request is proxied straight to the `httpdebug` backend whose body does **not** contain "anubis" (fails the grep). **Therefore all shipped starter policies must `CHALLENGE` browsers, not `ALLOW` them**, or the smoke pre-check fails for reasons unrelated to the browser.

---

## File Structure

**New files:**
- `chromesweep/policies/default.yaml` — starter Anubis ruleset (challenge everything).
- `chromesweep/policies/hard.yaml` — second starter ruleset (challenge with higher difficulty), proving the matrix runs >1 policy and names passes by filename.
- `chromesweep/policies.go` — `//go:embed policies/*.yaml`, the `Policy` type, and `LoadPolicies()`.
- `chromesweep/policies_test.go` — unit tests for `LoadPolicies`.

**Modified files:**
- `chromesweep/config.go` — add `Policies []Policy` to `Config`; `DefaultConfig` loads embedded policies.
- `chromesweep/config_test.go` — assert `DefaultConfig().Policies` is populated.
- `chromesweep/cluster.go` — add `CreateOrReplaceConfigMap`, `SnapshotPodTemplate`, `RestorePodTemplate`, `SetAnubisPolicy`, and the `upsert*` helpers + policy constants.
- `chromesweep/cluster_test.go` — unit tests for the new cluster methods (fake clientset).
- `chromesweep/report.go` — add `Policy` field to `Result`; add `PolicyStat`/`PolicyStats`; render a per-policy summary table and group results by policy.
- `chromesweep/report_test.go` — update existing assertions for the new markdown shape; add policy tests.
- `chromesweep/sweep.go` — restructure `Run` around the sequential policy loop; new `prepareAnubis` signature (snapshot/restore + deployment name); `applyPolicy`, `sweepBrowsers`, `policyErrorResults`; policy-namespaced frame names; thread policy into `sweepOne`.
- `chromesweep/sweep_test.go` — update `TestLocalFrameName` for the new 3-arg signature.

**Unchanged (verified):** `cmd/chrome-sweep/main.go` needs no code change — it builds `Config` from `DefaultConfig()` (which now carries policies) and prints `RenderMarkdown`, so the policy report flows through automatically. The smoke-job manifests are unchanged (all starter policies challenge). Task 8 is the live run.

---

## Task 1: Embedded policy folder + loader

**Files:**
- Create: `chromesweep/policies/default.yaml`
- Create: `chromesweep/policies/hard.yaml`
- Create: `chromesweep/policies.go`
- Test: `chromesweep/policies_test.go`

**Interfaces:**
- Produces:
  - `type Policy struct { Name string; Content []byte }` — `Name` is the filename without `.yaml`.
  - `func LoadPolicies() ([]Policy, error)` — reads all embedded `policies/*.yaml`, sorted by `Name`.

- [ ] **Step 1: Create the two starter policy files**

`chromesweep/policies/default.yaml`:

```yaml
# Baseline sweep policy: challenge every visitor with Anubis's proof-of-work.
# A real, non-headless browser (the whole point of this sweep) solves the
# challenge and reaches the backend; a bare HTTP client does not. The challenge
# interstitial itself contains "Anubis", so the smoke-job's `grep -qi anubis`
# pre-check passes under this policy.
bots:
  - name: generic-browser
    user_agent_regex: >-
      Mozilla|Opera
    action: CHALLENGE
  - name: everything-else
    user_agent_regex: .
    action: CHALLENGE
```

`chromesweep/policies/hard.yaml`:

```yaml
# Same shape as default, but pins a harder proof-of-work so this ruleset is a
# distinct test pass. Modern browsers still solve it well within the smoke Job's
# 90s capture timeout.
bots:
  - name: generic-browser
    user_agent_regex: >-
      Mozilla|Opera
    action: CHALLENGE
    challenge:
      algorithm: fast
      difficulty: 5
  - name: everything-else
    user_agent_regex: .
    action: CHALLENGE
```

- [ ] **Step 2: Write the failing test**

`chromesweep/policies_test.go`:

```go
package chromesweep

import "testing"

func TestLoadPolicies(t *testing.T) {
	got, err := LoadPolicies()
	if err != nil {
		t.Fatalf("LoadPolicies: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("want >= 2 embedded policies, got %d", len(got))
	}

	names := map[string]bool{}
	for i, p := range got {
		names[p.Name] = true
		if len(p.Content) == 0 {
			t.Fatalf("policy %q has empty content", p.Name)
		}
		// Name must be the filename without extension: no ".yaml", no slash.
		if got, bad := p.Name, ".yaml"; len(got) >= len(bad) && got[len(got)-len(bad):] == bad {
			t.Fatalf("policy name %q still has extension", p.Name)
		}
		// Sorted ascending by Name.
		if i > 0 && got[i-1].Name > p.Name {
			t.Fatalf("policies not sorted: %q before %q", got[i-1].Name, p.Name)
		}
	}
	for _, want := range []string{"default", "hard"} {
		if !names[want] {
			t.Fatalf("missing policy %q; have %v", want, names)
		}
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./chromesweep/ -run TestLoadPolicies -v`
Expected: FAIL — `undefined: LoadPolicies` (and `Policy`).

- [ ] **Step 4: Write `chromesweep/policies.go`**

```go
package chromesweep

import (
	"embed"
	"fmt"
	"path"
	"sort"
	"strings"
)

//go:embed policies/*.yaml
var policyFS embed.FS

// Policy is one embedded Anubis botPolicies ruleset. Name is the file's base name
// without the .yaml extension (e.g. "default"); it names the test pass in reports
// and the per-policy ConfigMap that carries the ruleset into the Anubis pod.
type Policy struct {
	Name    string
	Content []byte
}

// LoadPolicies reads every policies/*.yaml file embedded into the binary and
// returns them sorted by Name (the filename without its .yaml extension). Adding a
// new file to chromesweep/policies/ is all it takes to add a test pass.
func LoadPolicies() ([]Policy, error) {
	entries, err := policyFS.ReadDir("policies")
	if err != nil {
		return nil, fmt.Errorf("reading embedded policies: %w", err)
	}
	var policies []Policy
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		content, err := policyFS.ReadFile(path.Join("policies", e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading policy %s: %w", e.Name(), err)
		}
		policies = append(policies, Policy{
			Name:    strings.TrimSuffix(e.Name(), ".yaml"),
			Content: content,
		})
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].Name < policies[j].Name })
	return policies, nil
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./chromesweep/ -run TestLoadPolicies -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add chromesweep/policies chromesweep/policies.go chromesweep/policies_test.go
git commit -m "feat(chromesweep): embed Anubis policy rulesets + loader"
```

---

## Task 2: Config carries the embedded policies

**Files:**
- Modify: `chromesweep/config.go`
- Test: `chromesweep/config_test.go`

**Interfaces:**
- Consumes: `Policy`, `LoadPolicies()` (Task 1).
- Produces: `Config.Policies []Policy`, populated by `DefaultConfig()`.

- [ ] **Step 1: Write the failing test**

Add to `chromesweep/config_test.go`:

```go
func TestDefaultConfigLoadsPolicies(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Policies) < 2 {
		t.Fatalf("DefaultConfig should carry the embedded policies, got %d", len(cfg.Policies))
	}
	seen := map[string]bool{}
	for _, p := range cfg.Policies {
		seen[p.Name] = true
	}
	if !seen["default"] {
		t.Fatalf(`DefaultConfig missing the "default" policy; have %v`, seen)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./chromesweep/ -run TestDefaultConfigLoadsPolicies -v`
Expected: FAIL — `cfg.Policies undefined`.

- [ ] **Step 3: Add the `Policies` field and load it in `DefaultConfig`**

In `chromesweep/config.go`, change the imports from:

```go
import "time"
```

to:

```go
import (
	"fmt"
	"time"
)
```

Add the field to `Config` (after `Browsers`):

```go
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
	// Policies are the Anubis rulesets to sweep every browser against, one test
	// pass per policy. Defaults to the rulesets embedded under policies/.
	Policies []Policy
}
```

Add `Policies` to the `DefaultConfig` return and a helper below it:

```go
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
		Policies:        mustLoadPolicies(),
	}
}

// mustLoadPolicies loads the embedded policy rulesets, panicking on failure. The
// files are compiled into the binary, so an error here is a build/programming bug,
// not a runtime condition.
func mustLoadPolicies() []Policy {
	p, err := LoadPolicies()
	if err != nil {
		panic(fmt.Sprintf("loading embedded anubis policies: %v", err))
	}
	return p
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./chromesweep/ -run TestDefaultConfigLoadsPolicies -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add chromesweep/config.go chromesweep/config_test.go
git commit -m "feat(chromesweep): default config carries embedded policies"
```

---

## Task 3: Cluster support for ConfigMaps + Anubis policy wiring

**Files:**
- Modify: `chromesweep/cluster.go`
- Test: `chromesweep/cluster_test.go`

**Interfaces:**
- Consumes: existing `Cluster` (`c.cs kubernetes.Interface`, `c.ns string`), `corev1`, `appsv1`, `apierrors`, `metav1` (already imported in cluster.go).
- Produces:
  - Constants `anubisPolicyVolume = "anubis-policy"`, `anubisPolicyMountPath = "/policy"`, `anubisPolicyFileName = "botPolicies.yaml"`, `anubisPolicyEnvVar = "POLICY_FNAME"`.
  - `func (c *Cluster) CreateOrReplaceConfigMap(ctx, cm *corev1.ConfigMap) error`
  - `func (c *Cluster) SnapshotPodTemplate(ctx, deployment string) (*corev1.PodTemplateSpec, error)`
  - `func (c *Cluster) RestorePodTemplate(ctx, deployment string, tmpl *corev1.PodTemplateSpec) error`
  - `func (c *Cluster) SetAnubisPolicy(ctx, deployment, container, configMapName string) error`

- [ ] **Step 1: Write the failing tests**

Add to `chromesweep/cluster_test.go`:

```go
func TestCreateOrReplaceConfigMap(t *testing.T) {
	cs := fake.NewSimpleClientset()
	c := NewCluster(cs, "ci")
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "anubis-policy-default"},
		Data:       map[string]string{"botPolicies.yaml": "bots: []"},
	}
	if err := c.CreateOrReplaceConfigMap(context.Background(), cm); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Replace with new content: no error, content updated in place.
	cm2 := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "anubis-policy-default"},
		Data:       map[string]string{"botPolicies.yaml": "bots: [changed]"},
	}
	if err := c.CreateOrReplaceConfigMap(context.Background(), cm2); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err := cs.CoreV1().ConfigMaps("ci").Get(context.Background(), "anubis-policy-default", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Data["botPolicies.yaml"] != "bots: [changed]" {
		t.Fatalf("content = %q, want replaced", got.Data["botPolicies.yaml"])
	}
}

func TestSetAnubisPolicyAndRestore(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "anubis", Namespace: "ci"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "anubis", Image: "anubis:orig", Env: []corev1.EnvVar{{Name: "BIND", Value: ":8080"}}},
					},
				},
			},
		},
	}
	cs := fake.NewSimpleClientset(dep)
	c := NewCluster(cs, "ci")

	snap, err := c.SnapshotPodTemplate(context.Background(), "anubis")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	if err := c.SetAnubisPolicy(context.Background(), "anubis", "anubis", "anubis-policy-default"); err != nil {
		t.Fatalf("SetAnubisPolicy: %v", err)
	}
	got, _ := cs.AppsV1().Deployments("ci").Get(context.Background(), "anubis", metav1.GetOptions{})
	ct := got.Spec.Template.Spec.Containers[0]

	var envVal string
	for _, e := range ct.Env {
		if e.Name == "POLICY_FNAME" {
			envVal = e.Value
		}
	}
	if envVal != "/policy/botPolicies.yaml" {
		t.Fatalf("POLICY_FNAME = %q, want /policy/botPolicies.yaml", envVal)
	}
	if len(ct.VolumeMounts) != 1 || ct.VolumeMounts[0].Name != "anubis-policy" || ct.VolumeMounts[0].MountPath != "/policy" {
		t.Fatalf("volumeMount = %+v", ct.VolumeMounts)
	}
	vols := got.Spec.Template.Spec.Volumes
	if len(vols) != 1 || vols[0].Name != "anubis-policy" || vols[0].ConfigMap == nil || vols[0].ConfigMap.Name != "anubis-policy-default" {
		t.Fatalf("volumes = %+v", vols)
	}
	// The pre-existing env var must survive.
	found := false
	for _, e := range ct.Env {
		if e.Name == "BIND" {
			found = true
		}
	}
	if !found {
		t.Fatal("existing BIND env var was dropped")
	}

	// Re-applying a different policy must not duplicate volume/mount/env, only swap the CM name.
	if err := c.SetAnubisPolicy(context.Background(), "anubis", "anubis", "anubis-policy-hard"); err != nil {
		t.Fatalf("SetAnubisPolicy #2: %v", err)
	}
	got, _ = cs.AppsV1().Deployments("ci").Get(context.Background(), "anubis", metav1.GetOptions{})
	if v := got.Spec.Template.Spec.Volumes; len(v) != 1 || v[0].ConfigMap.Name != "anubis-policy-hard" {
		t.Fatalf("volumes after re-apply = %+v", v)
	}
	if m := got.Spec.Template.Spec.Containers[0].VolumeMounts; len(m) != 1 {
		t.Fatalf("volumeMounts duplicated: %+v", m)
	}

	// Restore returns the template to the original (no POLICY_FNAME, no policy volume).
	if err := c.RestorePodTemplate(context.Background(), "anubis", snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, _ = cs.AppsV1().Deployments("ci").Get(context.Background(), "anubis", metav1.GetOptions{})
	if len(got.Spec.Template.Spec.Volumes) != 0 {
		t.Fatalf("restore left volumes: %+v", got.Spec.Template.Spec.Volumes)
	}
	for _, e := range got.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "POLICY_FNAME" {
			t.Fatal("restore left POLICY_FNAME set")
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./chromesweep/ -run 'TestCreateOrReplaceConfigMap|TestSetAnubisPolicyAndRestore' -v`
Expected: FAIL — `c.CreateOrReplaceConfigMap undefined`, etc.

- [ ] **Step 3: Add the constants, methods, and helpers to `chromesweep/cluster.go`**

Append to the end of `chromesweep/cluster.go` (all needed imports — `context`, `fmt`, `corev1`, `appsv1`, `apierrors`, `metav1` — are already present):

```go
// Anubis policy wiring: a per-policy ConfigMap is mounted into the Anubis
// container at anubisPolicyMountPath and pointed at via the POLICY_FNAME env var.
const (
	anubisPolicyVolume    = "anubis-policy"
	anubisPolicyMountPath = "/policy"
	anubisPolicyFileName  = "botPolicies.yaml"
	anubisPolicyEnvVar    = "POLICY_FNAME"
)

// CreateOrReplaceConfigMap creates cm, or updates it in place if one of the same
// name already exists (ConfigMaps have no immutable spec to fight, so update is
// safe and avoids a delete/recreate race).
func (c *Cluster) CreateOrReplaceConfigMap(ctx context.Context, cm *corev1.ConfigMap) error {
	cm.Namespace = c.ns
	api := c.cs.CoreV1().ConfigMaps(c.ns)
	existing, err := api.Get(ctx, cm.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, cerr := api.Create(ctx, cm, metav1.CreateOptions{}); cerr != nil {
			return fmt.Errorf("creating configmap %s: %w", cm.Name, cerr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting configmap %s: %w", cm.Name, err)
	}
	cm.ResourceVersion = existing.ResourceVersion
	if _, uerr := api.Update(ctx, cm, metav1.UpdateOptions{}); uerr != nil {
		return fmt.Errorf("updating configmap %s: %w", cm.Name, uerr)
	}
	return nil
}

// SnapshotPodTemplate returns a deep copy of a Deployment's pod template so later
// image/policy edits can be reverted with RestorePodTemplate.
func (c *Cluster) SnapshotPodTemplate(ctx context.Context, deployment string) (*corev1.PodTemplateSpec, error) {
	d, err := c.cs.AppsV1().Deployments(c.ns).Get(ctx, deployment, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return d.Spec.Template.DeepCopy(), nil
}

// RestorePodTemplate sets a Deployment's pod template back to a snapshot, reverting
// any image/env/volume changes made during a sweep. The caller should
// WaitDeploymentReady afterward.
func (c *Cluster) RestorePodTemplate(ctx context.Context, deployment string, tmpl *corev1.PodTemplateSpec) error {
	api := c.cs.AppsV1().Deployments(c.ns)
	d, err := api.Get(ctx, deployment, metav1.GetOptions{})
	if err != nil {
		return err
	}
	d.Spec.Template = *tmpl
	if _, err := api.Update(ctx, d, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("restoring %s pod template: %w", deployment, err)
	}
	return nil
}

// SetAnubisPolicy points the named container of the Anubis Deployment at the policy
// file carried by configMapName. It upserts the POLICY_FNAME env var, a read-only
// volumeMount at anubisPolicyMountPath, and a ConfigMap-backed volume — so calling
// it repeatedly with different ConfigMap names only swaps the mounted file (no
// duplicated volumes/mounts). Changing configMapName changes the pod template, so
// the caller should WaitDeploymentReady after.
func (c *Cluster) SetAnubisPolicy(ctx context.Context, deployment, container, configMapName string) error {
	api := c.cs.AppsV1().Deployments(c.ns)
	d, err := api.Get(ctx, deployment, metav1.GetOptions{})
	if err != nil {
		return err
	}
	found := false
	for i := range d.Spec.Template.Spec.Containers {
		ct := &d.Spec.Template.Spec.Containers[i]
		if ct.Name != container {
			continue
		}
		found = true
		ct.Env = upsertEnv(ct.Env, anubisPolicyEnvVar, anubisPolicyMountPath+"/"+anubisPolicyFileName)
		ct.VolumeMounts = upsertVolumeMount(ct.VolumeMounts, corev1.VolumeMount{
			Name: anubisPolicyVolume, MountPath: anubisPolicyMountPath, ReadOnly: true,
		})
	}
	if !found {
		return fmt.Errorf("container %q not found in deployment %s", container, deployment)
	}
	d.Spec.Template.Spec.Volumes = upsertConfigMapVolume(d.Spec.Template.Spec.Volumes, anubisPolicyVolume, configMapName)
	if _, err := api.Update(ctx, d, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("setting anubis policy on %s: %w", deployment, err)
	}
	return nil
}

// upsertEnv sets name=value in env, replacing an existing entry (clearing any
// ValueFrom) or appending a new one.
func upsertEnv(env []corev1.EnvVar, name, value string) []corev1.EnvVar {
	for i := range env {
		if env[i].Name == name {
			env[i].Value = value
			env[i].ValueFrom = nil
			return env
		}
	}
	return append(env, corev1.EnvVar{Name: name, Value: value})
}

// upsertVolumeMount replaces the mount with the same Name, or appends it.
func upsertVolumeMount(mounts []corev1.VolumeMount, m corev1.VolumeMount) []corev1.VolumeMount {
	for i := range mounts {
		if mounts[i].Name == m.Name {
			mounts[i] = m
			return mounts
		}
	}
	return append(mounts, m)
}

// upsertConfigMapVolume sets vols[name] to a ConfigMap-backed volume for
// configMapName, replacing any existing volume of that Name or appending one.
func upsertConfigMapVolume(vols []corev1.Volume, name, configMapName string) []corev1.Volume {
	v := corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
			},
		},
	}
	for i := range vols {
		if vols[i].Name == name {
			vols[i] = v
			return vols
		}
	}
	return append(vols, v)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./chromesweep/ -run 'TestCreateOrReplaceConfigMap|TestSetAnubisPolicyAndRestore' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add chromesweep/cluster.go chromesweep/cluster_test.go
git commit -m "feat(chromesweep): configmap + anubis policy wiring on Cluster"
```

---

## Task 4: Report — per-policy field, summary, and grouping

**Files:**
- Modify: `chromesweep/report.go`
- Test: `chromesweep/report_test.go`

**Interfaces:**
- Consumes: existing `Result`, `Status`, `RenderMarkdown`.
- Produces:
  - `Result.Policy string` (JSON `policy,omitempty`).
  - `type PolicyStat struct { Name string; Passed int; Total int }` with `func (PolicyStat) Status() Status`.
  - `func PolicyStats(results []Result) []PolicyStat`.
  - `RenderMarkdown` now emits an `## Anubis policy results` table and groups by `# Policy: <name>` then `## <Browser> version sweep`.

- [ ] **Step 1: Update the existing tests for the new markdown shape and add policy tests**

In `chromesweep/report_test.go`, replace `TestRenderMarkdown` (lines 9–32) with a policy-aware version, and add two new tests. The new `TestRenderMarkdown`:

```go
func TestRenderMarkdown(t *testing.T) {
	md := RenderMarkdown(Report{
		AnubisImage: "reg/backend:v9",
		Results: []Result{
			{Policy: "default", Browser: "chrome", Tag: "150", Status: StatusPass, BrowserVersion: "150.0.7871.114", ReportedUA: "Chrome/150", FramePath: "var/sweep/default-chrome-150.png"},
			{Policy: "default", Browser: "chrome", Tag: "110", Status: StatusFail, Detail: "job failed"},
			{Policy: "default", Browser: "firefox", Tag: "152", Status: StatusPass, BrowserVersion: "152.0.5", FramePath: "var/sweep/default-firefox-152.png"},
			{Policy: "hard", Browser: "chrome", Tag: "150", Status: StatusPass, BrowserVersion: "150.0.7871.114"},
		},
	})
	for _, want := range []string{
		"## Anubis policy results",
		"| default | fail | 2/3 |",
		"| hard | pass | 1/1 |",
		"# Policy: default",
		"# Policy: hard",
		"## Chrome version sweep — 1/2 passed",
		"## Firefox version sweep — 1/1 passed",
		"| 150 |", "| 110 |", "job failed", "| 152 |",
		"Anubis image:", "reg/backend:v9",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
	// default policy section precedes hard (first-seen order).
	if strings.Index(md, "# Policy: default") > strings.Index(md, "# Policy: hard") {
		t.Fatalf("policy sections out of order:\n%s", md)
	}
}
```

Note the existing `TestRenderMarkdownEmbedsFailureLogs`, `TestRenderMarkdownOmitsAnubisWhenEmpty`, and `TestRenderJSON` (which use `Result`s with no `Policy` set) stay as-is — a `Result` with `Policy == ""` renders with no `# Policy:` header, and its browser section is still `## Firefox version sweep …`. **However** `TestRenderMarkdownEmbedsFailureLogs` asserts the `<details>` block content, which is unaffected. Leave those three tests unchanged.

Add these new tests at the end of `chromesweep/report_test.go`:

```go
func TestPolicyStats(t *testing.T) {
	stats := PolicyStats([]Result{
		{Policy: "default", Status: StatusPass},
		{Policy: "default", Status: StatusFail},
		{Policy: "hard", Status: StatusPass},
		{Policy: "hard", Status: StatusPass},
	})
	if len(stats) != 2 {
		t.Fatalf("want 2 policies, got %d: %+v", len(stats), stats)
	}
	if stats[0].Name != "default" || stats[0].Passed != 1 || stats[0].Total != 2 || stats[0].Status() != StatusFail {
		t.Fatalf("default stat wrong: %+v", stats[0])
	}
	if stats[1].Name != "hard" || stats[1].Passed != 2 || stats[1].Total != 2 || stats[1].Status() != StatusPass {
		t.Fatalf("hard stat wrong: %+v", stats[1])
	}
}

func TestResultPolicyRoundTrips(t *testing.T) {
	b, err := RenderJSON(Report{Results: []Result{{Policy: "hard", Browser: "chrome", Tag: "150", Status: StatusPass}}})
	if err != nil {
		t.Fatal(err)
	}
	var out Report
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.Results[0].Policy != "hard" {
		t.Fatalf("policy round-trip = %q", out.Results[0].Policy)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./chromesweep/ -run 'TestRenderMarkdown$|TestPolicyStats|TestResultPolicyRoundTrips' -v`
Expected: FAIL — `Result.Policy` and `PolicyStats` undefined; markdown lacks the new sections.

- [ ] **Step 3: Add the `Policy` field to `Result`**

In `chromesweep/report.go`, add `Policy` as the first field of `Result`:

```go
type Result struct {
	// Policy is the Anubis ruleset this version was tested against (the policy
	// filename without extension). Empty when the sweep used Anubis's live policy.
	Policy         string `json:"policy,omitempty"`
	Browser        string `json:"browser,omitempty"`
	Tag            string `json:"tag"`
	Status         Status `json:"status"`
	BrowserVersion string `json:"browser_version,omitempty"`
	ReportedUA     string `json:"reported_ua,omitempty"`
	FramePath      string `json:"frame_path,omitempty"`
	Detail         string `json:"detail,omitempty"`
	Logs []LogCapture `json:"-"`
}
```

- [ ] **Step 4: Add `PolicyStat` / `PolicyStats`**

Add after the `AllPassed` method in `chromesweep/report.go`:

```go
// PolicyStat is the pass tally for one Anubis policy across all browsers/versions.
type PolicyStat struct {
	Name   string
	Passed int
	Total  int
}

// Status is pass when every version under the policy passed, else fail.
func (p PolicyStat) Status() Status {
	if p.Total > 0 && p.Passed == p.Total {
		return StatusPass
	}
	return StatusFail
}

// PolicyStats tallies pass/total per policy in first-seen order.
func PolicyStats(results []Result) []PolicyStat {
	idx := map[string]int{}
	var stats []PolicyStat
	for _, r := range results {
		i, ok := idx[r.Policy]
		if !ok {
			i = len(stats)
			idx[r.Policy] = i
			stats = append(stats, PolicyStat{Name: r.Policy})
		}
		stats[i].Total++
		if r.Status == StatusPass {
			stats[i].Passed++
		}
	}
	return stats
}
```

- [ ] **Step 5: Rewrite `RenderMarkdown` to summarize + group by policy**

Replace the existing `RenderMarkdown` (lines 60–99) with:

```go
// RenderMarkdown produces a human-readable summary: an Anubis-policy pass/fail
// table, then one section per policy (first-seen order), each grouping its
// browsers and their per-version results.
func RenderMarkdown(rep Report) string {
	var b strings.Builder
	if rep.AnubisImage != "" {
		fmt.Fprintf(&b, "Anubis image: `%s`\n\n", rep.AnubisImage)
	}
	if stats := PolicyStats(rep.Results); len(stats) > 0 {
		b.WriteString("## Anubis policy results\n\n")
		b.WriteString("| policy | status | versions passed |\n")
		b.WriteString("|--------|--------|-----------------|\n")
		for _, s := range stats {
			fmt.Fprintf(&b, "| %s | %s | %d/%d |\n", dash(s.Name), s.Status(), s.Passed, s.Total)
		}
		b.WriteString("\n")
	}
	var order []string
	byPolicy := map[string][]Result{}
	for _, r := range rep.Results {
		if _, ok := byPolicy[r.Policy]; !ok {
			order = append(order, r.Policy)
		}
		byPolicy[r.Policy] = append(byPolicy[r.Policy], r)
	}
	for _, pol := range order {
		if pol != "" {
			fmt.Fprintf(&b, "# Policy: %s\n\n", pol)
		}
		renderBrowserGroups(&b, byPolicy[pol])
	}
	return b.String()
}

// renderBrowserGroups renders one browser section per browser (first-seen order)
// for results already scoped to a single policy: a header with the pass count, a
// results table, then collapsed failure-log blocks for any non-passing run.
func renderBrowserGroups(b *strings.Builder, results []Result) {
	var order []string
	groups := map[string][]Result{}
	for _, r := range results {
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
		fmt.Fprintf(b, "## %s version sweep — %d/%d passed\n\n", titleCase(br), passed, len(rs))
		b.WriteString("| tag | status | browser version | frame | detail |\n")
		b.WriteString("|-----|--------|-----------------|-------|--------|\n")
		for _, r := range rs {
			fmt.Fprintf(b, "| %s | %s | %s | %s | %s |\n",
				r.Tag, r.Status, dash(r.BrowserVersion), dash(r.FramePath), dash(r.Detail))
		}
		b.WriteString("\n")
		for _, r := range rs {
			if r.Status == StatusPass || len(r.Logs) == 0 {
				continue
			}
			writeFailureLogs(b, r)
		}
	}
}
```

- [ ] **Step 6: Run the full report test file**

Run: `go test ./chromesweep/ -run 'Render|Policy' -v`
Expected: PASS (including the unchanged `TestRenderMarkdownEmbedsFailureLogs`, `TestRenderMarkdownOmitsAnubisWhenEmpty`, `TestRenderJSON`).

- [ ] **Step 7: Commit**

```bash
git add chromesweep/report.go chromesweep/report_test.go
git commit -m "feat(chromesweep): per-policy report field, summary, and grouping"
```

---

## Task 5: Sweep orchestration — sequential policy loop

**Files:**
- Modify: `chromesweep/sweep.go`
- Test: `chromesweep/sweep_test.go`

**Interfaces:**
- Consumes: `Config.Policies` (Task 2); `Cluster.CreateOrReplaceConfigMap`, `SnapshotPodTemplate`, `RestorePodTemplate`, `SetAnubisPolicy`, `WaitDeploymentReady`, `SetImage` (Task 3); `anubisPolicyFileName` (Task 3); `Result.Policy` (Task 4).
- Produces: restructured `Run`; `prepareAnubis(...) (image, deployment string, restore func(), err error)`; `applyPolicy`, `sweepBrowsers`, `policyErrorResults`; `sweepOne(..., policy string)`; `localFrameName(policy, browser, tag string)`.

- [ ] **Step 1: Update `TestLocalFrameName` for the new 3-arg signature**

Replace `chromesweep/sweep_test.go` entirely:

```go
package chromesweep

import "testing"

func TestLocalFrameName(t *testing.T) {
	// Policy + browser + tag are all part of the on-disk frame name so nothing
	// collides across the policy × browser × version matrix.
	if got := localFrameName("default", "chrome", "130"); got != "default-chrome-130.png" {
		t.Fatalf("chrome: %q", got)
	}
	if got := localFrameName("default", "firefox", "130"); got != "default-firefox-130.png" {
		t.Fatalf("firefox: %q", got)
	}
	if got := localFrameName("hard", "chrome", "130"); got != "hard-chrome-130.png" {
		t.Fatalf("hard policy: %q", got)
	}
	// Empty policy (live-policy fallback) omits the prefix.
	if got := localFrameName("", "chrome", "130"); got != "chrome-130.png" {
		t.Fatalf("empty policy: %q", got)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./chromesweep/ -run TestLocalFrameName -v`
Expected: FAIL — `too many arguments in call to localFrameName`.

- [ ] **Step 3: Update imports in `chromesweep/sweep.go`**

Add `metav1` to the import block (corev1 is already imported):

```go
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)
```

- [ ] **Step 4: Replace `Run` (lines 53–96) with the policy-looping version**

```go
// Run sweeps every browser in cfg.Browsers against every policy in cfg.Policies.
// Anubis is a shared singleton, so policies run SEQUENTIALLY: for each policy the
// ruleset is written to a ConfigMap, wired into the live Anubis Deployment
// (POLICY_FNAME + mount), rolled out, then the browser/version matrix runs against
// it in a bounded pool of cfg.Parallelism. Every Result is tagged with its policy.
// The frame collector is created once; the original Anubis pod template is restored
// at the end. A failure on one version (or one whole policy) is recorded and does
// not stop the rest.
func Run(ctx context.Context, c *Cluster, cfg Config, framesDir string) (Report, error) {
	anubisImage, anubisDeployment, restoreAnubis, err := prepareAnubis(ctx, c, cfg)
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

	policies := cfg.Policies
	if len(policies) == 0 {
		// No embedded policies: sweep once against whatever policy Anubis is already
		// running, tagging results with an empty policy name.
		policies = []Policy{{Name: ""}}
	}

	var results []Result
	for _, pol := range policies {
		if pol.Name != "" {
			if err := applyPolicy(ctx, c, cfg, anubisDeployment, pol); err != nil {
				slog.Warn("applying anubis policy failed; marking its versions errored",
					"policy", pol.Name, "err", err)
				results = append(results, policyErrorResults(cfg, pol.Name, err)...)
				continue
			}
		}
		results = append(results, sweepBrowsers(ctx, c, cfg, framesDir, pol.Name)...)
	}
	return Report{AnubisImage: anubisImage, Results: results}, nil
}

// applyPolicy renders pol into a per-policy ConfigMap, wires it into the Anubis
// Deployment, and waits for the resulting rollout so subsequent browser requests
// hit the new ruleset.
func applyPolicy(ctx context.Context, c *Cluster, cfg Config, deployment string, pol Policy) error {
	cmName := "anubis-policy-" + pol.Name
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: cfg.Namespace},
		Data:       map[string]string{anubisPolicyFileName: string(pol.Content)},
	}
	if err := c.CreateOrReplaceConfigMap(ctx, cm); err != nil {
		return fmt.Errorf("policy configmap: %w", err)
	}
	if err := c.SetAnubisPolicy(ctx, deployment, cfg.AnubisContainer, cmName); err != nil {
		return fmt.Errorf("wiring policy into anubis: %w", err)
	}
	if err := c.WaitDeploymentReady(ctx, deployment, cfg.ReadyTimeout); err != nil {
		return fmt.Errorf("anubis rollout for policy %s: %w", pol.Name, err)
	}
	slog.Info("applied anubis policy", "policy", pol.Name, "configmap", cmName)
	return nil
}

// policyErrorResults synthesizes an errored Result for every browser/version under
// a policy that could not be applied (e.g. Anubis rejected the ruleset and never
// became ready), so a bad policy shows as a clean per-policy failure in the report.
func policyErrorResults(cfg Config, policy string, cause error) []Result {
	var out []Result
	for _, b := range cfg.Browsers {
		for _, tag := range b.Versions {
			out = append(out, Result{
				Policy:  policy,
				Browser: b.Name,
				Tag:     tag,
				Status:  StatusError,
				Detail:  cause.Error(),
			})
		}
	}
	return out
}

// sweepBrowsers runs the full browser/version matrix against the currently-live
// Anubis policy, tagging each Result with policy. Versions run in a bounded pool;
// a manifest-load failure for one browser errors only that browser's versions.
func sweepBrowsers(ctx context.Context, c *Cluster, cfg Config, framesDir, policy string) []Result {
	parallelism := cfg.Parallelism
	if parallelism < 1 {
		parallelism = 1
	}
	var results []Result
	for _, b := range cfg.Browsers {
		base, err := loadBaseManifests(b, cfg.Namespace)
		if err != nil {
			for _, tag := range b.Versions {
				results = append(results, Result{
					Policy: policy, Browser: b.Name, Tag: tag,
					Status: StatusError, Detail: err.Error(),
				})
			}
			continue
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
				brResults[i] = sweepOne(ctx, c, cfg, b, base, tag, framesDir, policy)
			}(i, tag)
		}
		wg.Wait()
		results = append(results, brResults...)
	}
	return results
}
```

- [ ] **Step 5: Replace `prepareAnubis` (lines 98–137) with the snapshot/restore version**

```go
// prepareAnubis resolves the Anubis image the sweep runs against and snapshots the
// live Anubis pod template so any per-policy edits (and an optional image override)
// can be reverted afterward. It returns the resolved image, the Anubis Deployment
// name, and a restore func. If cfg.AnubisImage is set, the live Deployment is
// re-imaged for the run.
func prepareAnubis(ctx context.Context, c *Cluster, cfg Config) (image, deployment string, restore func(), err error) {
	noop := func() {}
	dep, err := loadDeployment(cfg.AnubisManifest, cfg.Namespace)
	if err != nil {
		return "", "", noop, fmt.Errorf("loading anubis manifest: %w", err)
	}
	manifestImage := ""
	for _, ct := range dep.Spec.Template.Spec.Containers {
		if ct.Name == cfg.AnubisContainer {
			manifestImage = ct.Image
		}
	}
	// Snapshot the live pod template; the restore reverts image + policy wiring in one shot.
	snapshot, err := c.SnapshotPodTemplate(ctx, dep.Name)
	if err != nil {
		return "", "", noop, fmt.Errorf("snapshotting anubis: %w", err)
	}
	restore = func() {
		if rerr := c.RestorePodTemplate(context.Background(), dep.Name, snapshot); rerr != nil {
			slog.Warn("restoring anubis pod template failed", "err", rerr)
			return
		}
		if rerr := c.WaitDeploymentReady(context.Background(), dep.Name, cfg.ReadyTimeout); rerr != nil {
			slog.Warn("waiting for anubis restore rollout failed", "err", rerr)
		}
	}

	image = manifestImage
	if cfg.AnubisImage != "" {
		if err := c.SetImage(ctx, dep.Name, cfg.AnubisContainer, cfg.AnubisImage); err != nil {
			restore()
			return "", "", noop, fmt.Errorf("setting anubis image: %w", err)
		}
		slog.Info("re-imaged anubis for the sweep", "deployment", dep.Name, "image", cfg.AnubisImage)
		if err := c.WaitDeploymentReady(ctx, dep.Name, cfg.ReadyTimeout); err != nil {
			restore()
			return "", "", noop, fmt.Errorf("anubis rollout: %w", err)
		}
		image = cfg.AnubisImage
	}
	return image, dep.Name, restore, nil
}
```

- [ ] **Step 6: Thread `policy` into `sweepOne` and namespace the frame name**

In `sweepOne`, change the signature and set `Policy` on the result:

```go
func sweepOne(ctx context.Context, c *Cluster, cfg Config, b Browser, base baseManifests, tag, framesDir, policy string) Result {
	name := versionedName(b.Deployment, tag) // e.g. chrome-150 / firefox-152
	jobName := versionedName(b.JobName, tag) // e.g. chrome-smoke-150
	image := fmt.Sprintf("%s:%s", b.ImageRepo, tag)
	res := Result{Policy: policy, Browser: b.Name, Tag: tag}
	log := slog.With("browser", b.Name, "tag", tag, "image", image, "name", name, "policy", policy)
	log.Info("testing version")
```

Then, in the frame-copy block inside `sweepOne`, change the `local` path to include the policy:

```go
				local := filepath.Join(framesDir, localFrameName(policy, b.Name, tag))
```

- [ ] **Step 7: Update `localFrameName` (lines 237–239) to include the policy**

```go
// localFrameName is the on-disk name for a captured frame, namespaced by policy and
// browser so nothing collides across the policy × browser × version matrix. An empty
// policy (live-policy fallback) omits the prefix.
func localFrameName(policy, browser, tag string) string {
	if policy == "" {
		return browser + "-" + tag + ".png"
	}
	return policy + "-" + browser + "-" + tag + ".png"
}
```

- [ ] **Step 8: Run the package tests + vet**

Run: `go test ./chromesweep/ -v` then `go vet ./...`
Expected: PASS, no vet complaints. (The orchestration in `Run` is not unit-tested; the pure/unit pieces all pass.)

- [ ] **Step 9: Build every binary to confirm nothing else broke**

Run: `go build -o ./var/chrome-sweep ./cmd/chrome-sweep && go build ./...`
Expected: builds clean. (`./var/chrome-sweep` exists; no stray binary in the repo root.)

- [ ] **Step 10: Commit**

```bash
git add chromesweep/sweep.go chromesweep/sweep_test.go
git commit -m "feat(chromesweep): sweep every browser against each embedded policy"
```

---

## Task 6: Full package + module test sweep

**Files:**
- Test only (no code changes expected).

- [ ] **Step 1: Run the whole module's tests with the race detector**

Run: `go test -race ./...`
Expected: PASS across all packages (`chromesweep`, `cmd/...`, `cmd/gubald/svc/smoketest`). If `cmd/gubald/svc/smoketest` fails, it is unrelated to markdown shape (it validates the Twirp request, not report text) — investigate before proceeding.

- [ ] **Step 2: Vet the module**

Run: `go vet ./...`
Expected: clean.

- [ ] **Step 3: Commit only if anything needed fixing** (otherwise skip)

```bash
git add -A
git commit -m "test: green module test sweep for anubis policy matrix"
```

---

## Task 7: Docs — README note on adding policies

**Files:**
- Modify: `k8s/anubis/README.md` (append a short section) OR create `chromesweep/policies/README.md`.

Use `chromesweep/policies/README.md` (co-located with the files it documents):

- [ ] **Step 1: Create `chromesweep/policies/README.md`**

```markdown
# Anubis sweep policies

Each `*.yaml` here is a full [Anubis](https://github.com/TecharoHQ/anubis)
`botPolicies` ruleset. They are compiled into the `chromesweep` binary via
`go:embed`, and every sweep runs the full browser × version matrix against **each**
ruleset as a separate test pass named by the file's base name (e.g. `default.yaml`
→ the `default` pass).

To add a pass: drop a new `*.yaml` file in this directory and rebuild. No code
change is needed — `LoadPolicies()` discovers it.

Constraints:

- The filename (without `.yaml`) becomes a Kubernetes ConfigMap name
  (`anubis-policy-<name>`), so use DNS-safe names: lowercase letters, digits, `-`.
- Rulesets must **CHALLENGE** browser user-agents, not `ALLOW` them: the smoke Job
  asserts the Anubis challenge page (which contains "Anubis") is served. An
  `ALLOW`ed request is proxied to the backend, whose body lacks "Anubis", and the
  pre-check would fail for reasons unrelated to the browser.
- A ruleset that Anubis rejects makes its pod crashloop; that policy's rollout
  times out and every version under it is reported as `error`.
```

- [ ] **Step 2: Commit**

```bash
git add chromesweep/policies/README.md
git commit -m "docs(chromesweep): how to add Anubis sweep policies"
```

---

## Task 8: Live integration run (Chrome 150 + Firefox 152)

**Files:** none (manual verification against the real cluster).

This is the real-cluster acceptance test the requester asked for. It needs a working `KUBECONFIG` pointing at the cluster whose `ci` namespace runs Anubis, plus the `chrome-150` and `firefox-152` images present in GHCR. Only two versions are swept so iteration is fast.

- [ ] **Step 1: Build the CLI into `./var`**

Run: `go build -o ./var/chrome-sweep ./cmd/chrome-sweep`
Expected: builds clean.

- [ ] **Step 2: Run the sweep against two versions and both policies**

Run (from the repo root, so the `k8s/*.yaml` manifests resolve):

```bash
./var/chrome-sweep -chrome-versions 150 -firefox-versions 152 -out ./var/sweep
```

Expected: the process runs `default` then `hard` policies sequentially (2 policies × 2 browsers × 1 version = 4 smoke Jobs total), prints a markdown report to stdout ending with an `## Anubis policy results` table listing `default` and `hard`, and writes `./var/sweep/report.md` and `./var/sweep/report.zip`. Exit code 0 iff every version passed under every policy.

- [ ] **Step 3: Inspect the report**

Run: `sed -n '1,40p' ./var/sweep/report.md`
Expected: an `Anubis image:` line, the `## Anubis policy results` table (`default`/`hard`, each `2/2` on success), and `# Policy: default` / `# Policy: hard` sections each with `## Chrome version sweep — 1/1 passed` and `## Firefox version sweep — 1/1 passed`.

- [ ] **Step 4: If a policy is reported `error` (Anubis rejected the ruleset), diagnose and fix the YAML**

Run: `kubectl -n ci logs deploy/anubis -c anubis --tail=50`
Look for a config-parse/validation error. Adjust the offending `chromesweep/policies/*.yaml` to match the running Anubis version's schema (see the schema notes in Global Constraints — only `bots` is required; `action` ∈ ALLOW/WEIGH/CHALLENGE/DENY; per-rule `challenge:` supports `algorithm` and `difficulty`). Rebuild (`go build -o ./var/chrome-sweep ./cmd/chrome-sweep`) and re-run Step 2. Repeat until both policies pass.

- [ ] **Step 5: Confirm Anubis was restored**

Run: `kubectl -n ci get deploy anubis -o jsonpath='{.spec.template.spec.containers[?(@.name=="anubis")].env[*].name}'; echo`
Expected: **no** `POLICY_FNAME` in the output (the snapshot restore reverted the policy wiring). Also `kubectl -n ci get cm | grep anubis-policy` will show the per-policy ConfigMaps left behind — that is expected (they are harmless and reused next run); delete them manually if desired.

- [ ] **Step 6: Commit any policy-YAML fixes made in Step 4**

```bash
git add chromesweep/policies
git commit -m "fix(chromesweep): align sweep policies with live Anubis schema"
```

---

## Self-Review

**1. Spec coverage:**
- "folder of anubis rules in package chromesweep that gets compiled in" → Task 1 (`chromesweep/policies/*.yaml` + `//go:embed`). ✅
- "each browser is tested against that anubis.yaml file" → Task 5 (`Run` policy loop × `sweepBrowsers`). ✅
- "report would also include a list of the anubis policy files that passed and failed" → Task 4 (`PolicyStats` + `## Anubis policy results` table). ✅
- "test pass named by the ruleset name without extension" → Task 1 (`strings.TrimSuffix(name, ".yaml")`) + Task 4/5 (policy threaded to `Result.Policy`, rendered as `# Policy: <name>`). ✅
- "add arbitrary numbers of anubis rulesets to that folder" → Task 1 (`ReadDir` glob discovery) + Task 7 (documented). ✅
- "make a configmap … set the anubis envvar POLICY_FNAME to the mounted path" → Task 3 (`CreateOrReplaceConfigMap` + `SetAnubisPolicy` sets `POLICY_FNAME=/policy/botPolicies.yaml` + mount). ✅
- "test using cmd/chrome-sweep with chrome 150 and firefox 152, only two versions" → Task 8. ✅

**2. Placeholder scan:** No `TBD`/`handle edge cases`/"write tests for the above" — every code step shows complete code, every test step shows the test, every run step shows the command and expected output. ✅

**3. Type consistency:**
- `Policy{Name, Content}` defined in Task 1, consumed in Tasks 2/5 identically.
- `Config.Policies []Policy` (Task 2) consumed by `Run` (Task 5).
- `Result.Policy string` (Task 4) set in `sweepOne`/`sweepBrowsers`/`policyErrorResults` (Task 5) and read by `PolicyStats`/`RenderMarkdown` (Task 4).
- `localFrameName(policy, browser, tag)` — 3-arg form defined in Task 5 Step 7, matching the test in Task 5 Step 1 and the call in Step 6.
- Cluster methods `CreateOrReplaceConfigMap`, `SnapshotPodTemplate`, `RestorePodTemplate`, `SetAnubisPolicy` (Task 3) called with matching signatures in `applyPolicy`/`prepareAnubis` (Task 5).
- `anubisPolicyFileName` constant (Task 3) used in `applyPolicy` (Task 5). ✅

All consistent.
