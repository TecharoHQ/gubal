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
