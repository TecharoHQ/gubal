package chromesweep

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

const collectorPodName = "chrome-sweep-collector"

// baseManifests holds the decoded template objects, loaded once and deep-copied
// per version before retargeting.
type baseManifests struct {
	deployment *appsv1.Deployment
	service    *corev1.Service
	netpol     *networkingv1.NetworkPolicy
	job        *batchv1.Job
}

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
		// No policies configured: sweep once against whatever policy Anubis is
		// already running, tagging results with an empty policy name.
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

func sweepOne(ctx context.Context, c *Cluster, cfg Config, b Browser, base baseManifests, tag, framesDir, policy string) Result {
	name := versionedName(b.Deployment, tag) // e.g. chrome-150 / firefox-152
	jobName := versionedName(b.JobName, tag) // e.g. chrome-smoke-150
	image := fmt.Sprintf("%s:%s", b.ImageRepo, tag)
	res := Result{Policy: policy, Browser: b.Name, Tag: tag}
	log := slog.With("browser", b.Name, "tag", tag, "image", image, "name", name, "policy", policy)
	log.Info("testing version")

	// Tear this version's resources down when done, even on early return. Uses a
	// fresh context so cleanup runs even if ctx was cancelled.
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

	smokeLogs, smokeErr := c.JobContainerLogs(ctx, jobName, "smoke")
	if smokeErr == nil {
		res.ReportedUA = reportedUA(smokeLogs)
	} else {
		log.Warn("reading smoke logs failed", "err", smokeErr)
	}
	bullyLogs, bullyErr := c.JobContainerLogs(ctx, jobName, "chrome-bully")
	if bullyErr == nil {
		if remote, perr := capturedFramePath(bullyLogs); perr == nil {
			res.BrowserVersion = versionFromFrame(remote)
			local := filepath.Join(framesDir, localFrameName(policy, b.Name, tag))
			if cerr := c.CopyFrame(ctx, collectorPodName, remote, local); cerr == nil {
				res.FramePath = local
			} else {
				log.Warn("frame copy failed", "err", cerr)
			}
		}
	} else {
		log.Warn("reading chrome-bully logs failed", "err", bullyErr)
	}
	// Browser-side (foxbridge/Firefox or Chrome) logs — fetched before the deferred
	// teardown deletes the Deployment. Best-effort; a missing log is warned, not fatal.
	browserLogs, browserErr := c.DeploymentPodLogs(ctx, name, b.Container)
	if browserErr != nil {
		log.Warn("reading browser logs failed", "err", browserErr)
	}
	// Bundle the captured logs, browser-side first. Empty captures are dropped.
	for _, lc := range []LogCapture{
		{Container: b.Container, Content: browserLogs},
		{Container: "chrome-bully", Content: bullyLogs},
		{Container: "smoke", Content: smokeLogs},
	} {
		if lc.Content != "" {
			res.Logs = append(res.Logs, lc)
		}
	}

	if ok {
		res.Status = StatusPass
	} else {
		res.Status, res.Detail = StatusFail, "smoke job failed"
	}
	return res
}

// localFrameName is the on-disk name for a captured frame, namespaced by policy and
// browser so nothing collides across the policy × browser × version matrix. An empty
// policy (live-policy fallback) omits the prefix.
func localFrameName(policy, browser, tag string) string {
	if policy == "" {
		return browser + "-" + tag + ".png"
	}
	return policy + "-" + browser + "-" + tag + ".png"
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
