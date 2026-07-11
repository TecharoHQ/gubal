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
