// Command chrome-sweep tests a list of Chrome image tags in bounded parallel: for
// each tag it creates a per-version chrome Deployment/Service/NetworkPolicy and
// smoke Job (all named chrome-<tag>), waits for rollout, runs the smoke Job,
// records a pass/fail + screenshot, then tears the version's resources down. One
// shared PVC collects every version's frames.
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
	Namespace             string
	Deployment            string
	Container             string
	ImageRepo             string
	JobManifest           string
	JobName               string
	DeploymentManifest    string
	ServiceManifest       string
	NetworkPolicyManifest string
	AnubisManifest        string
	AnubisContainer       string
	AnubisImage           string
	Parallelism           int
	CollectorPVC          string
	OutDir                string
	ReadyTimeout          time.Duration
	JobTimeout            time.Duration
	Versions              []string
}

func main() {
	var (
		kubeconfig = flag.String("kubeconfig", defaultKubeconfig(), "path to kubeconfig")
		cfg        = Config{}
	)
	flag.StringVar(&cfg.Namespace, "namespace", "ci", "namespace holding the chrome Deployment and smoke Job")
	flag.StringVar(&cfg.Deployment, "deployment", "chrome", "base name for per-version chrome resources (Deployment/Service/NetworkPolicy)")
	flag.StringVar(&cfg.Container, "container", "chrome", "container within the Deployment to re-image")
	flag.StringVar(&cfg.ImageRepo, "image-repo", "ghcr.io/techarohq/gubal/chrome", "image repository; final ref is <repo>:<tag>")
	flag.StringVar(&cfg.JobManifest, "job-manifest", "k8s/smoke-job.yaml", "path to the smoke Job manifest to run each version")
	flag.StringVar(&cfg.JobName, "job-name", "chrome-smoke", "base name for per-version smoke Jobs; per version appends -<tag>")
	flag.StringVar(&cfg.DeploymentManifest, "deployment-manifest", "k8s/deployment.yaml", "base Deployment manifest to template per version")
	flag.StringVar(&cfg.ServiceManifest, "service-manifest", "k8s/service.yaml", "base Service manifest to template per version")
	flag.StringVar(&cfg.NetworkPolicyManifest, "networkpolicy-manifest", "k8s/networkpolicy.yaml", "base NetworkPolicy manifest to template per version")
	flag.StringVar(&cfg.AnubisManifest, "anubis-manifest", "k8s/anubis/anubis.yaml", "Anubis Deployment manifest; the tested Anubis image ref is read from it")
	flag.StringVar(&cfg.AnubisContainer, "anubis-container", "anubis", "container in the Anubis Deployment that holds the Anubis image")
	flag.StringVar(&cfg.AnubisImage, "anubis-image", "", "override the Anubis image ref (default: the ref from -anubis-manifest); when set, the live Anubis Deployment is re-imaged for the run and restored after")
	flag.IntVar(&cfg.Parallelism, "parallelism", 8, "max number of versions tested concurrently")
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

	// Frames are copied into a scratch dir, bundled into report.zip, then removed —
	// report.zip is the only artifact left in OutDir.
	framesDir, err := os.MkdirTemp("", "chrome-sweep-frames-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(framesDir)

	rep, err := Run(ctx, cluster, cfg, framesDir)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return err
	}
	js, err := renderJSON(rep)
	if err != nil {
		return err
	}
	if err := writeBundle(filepath.Join(cfg.OutDir, "report.zip"), js, rep.Results); err != nil {
		return err
	}
	fmt.Print(renderMarkdown(rep))

	for _, r := range rep.Results {
		if r.Status != StatusPass {
			return fmt.Errorf("one or more versions did not pass; see %s/report.zip", cfg.OutDir)
		}
	}
	return nil
}
