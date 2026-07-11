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

	versions, err := parseVersions(flag.Args())
	if err != nil {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
		slog.Error("bad versions", "err", err)
		os.Exit(1)
	}
	cfg.Versions = versions

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
