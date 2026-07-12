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
// once) and returns a Report: one Result per version (in argument order) plus the
// Anubis image they were tested against. Captured frames are copied into framesDir
// (a scratch dir the caller owns and bundles/cleans up). A failure on one version
// is recorded and does not stop the others.
func Run(ctx context.Context, c *Cluster, cfg Config, framesDir string) (Report, error) {
	base, err := loadBaseManifests(cfg)
	if err != nil {
		return Report{}, err
	}

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
	results := make([]Result, len(cfg.Versions))
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	for i, tag := range cfg.Versions {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, tag string) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = sweepOne(ctx, c, cfg, base, tag, framesDir)
		}(i, tag)
	}
	wg.Wait()
	return Report{AnubisImage: anubisImage, Results: results}, nil
}

// prepareAnubis resolves the Anubis image the sweep runs against. The default ref
// is read from the Anubis manifest (never hardcoded); if cfg.AnubisImage is set it
// overrides that and the live Anubis Deployment is re-imaged for the run, with the
// returned restore func putting the original image back afterward.
func prepareAnubis(ctx context.Context, c *Cluster, cfg Config) (image string, restore func(), err error) {
	noop := func() {}
	dep, err := loadDeployment(cfg.AnubisManifest, cfg.Namespace)
	if err != nil {
		return "", noop, fmt.Errorf("loading anubis manifest: %w", err)
	}
	manifestImage := ""
	for _, ct := range dep.Spec.Template.Spec.Containers {
		if ct.Name == cfg.AnubisContainer {
			manifestImage = ct.Image
		}
	}
	if cfg.AnubisImage == "" {
		// No override: report the manifest's declared image; leave the cluster alone.
		return manifestImage, noop, nil
	}
	// Override: re-image the live Anubis Deployment and restore it afterward.
	original, err := c.ContainerImage(ctx, dep.Name, cfg.AnubisContainer)
	if err != nil {
		return "", noop, fmt.Errorf("reading current anubis image: %w", err)
	}
	if err := c.SetImage(ctx, dep.Name, cfg.AnubisContainer, cfg.AnubisImage); err != nil {
		return "", noop, fmt.Errorf("setting anubis image: %w", err)
	}
	restore = func() {
		if rerr := c.SetImage(context.Background(), dep.Name, cfg.AnubisContainer, original); rerr != nil {
			slog.Warn("restoring anubis image failed", "err", rerr, "image", original)
		}
	}
	slog.Info("re-imaged anubis for the sweep", "deployment", dep.Name, "image", cfg.AnubisImage)
	if err := c.WaitDeploymentReady(ctx, dep.Name, cfg.ReadyTimeout); err != nil {
		restore()
		return "", noop, fmt.Errorf("anubis rollout: %w", err)
	}
	return cfg.AnubisImage, restore, nil
}

func sweepOne(ctx context.Context, c *Cluster, cfg Config, base baseManifests, tag, framesDir string) Result {
	name := versionedName(cfg.Deployment, tag) // chrome-<tag>
	jobName := versionedName(cfg.JobName, tag) // chrome-smoke-<tag>
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
			local := filepath.Join(framesDir, tag+".png")
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
