// Command chrome-sweep tests a list of Chrome image tags in bounded parallel: for
// each tag it creates a per-version chrome Deployment/Service/NetworkPolicy and
// smoke Job (all named chrome-<tag>), waits for rollout, runs the smoke Job,
// records a pass/fail + screenshot, then tears the version's resources down. It is
// a thin CLI over the importable github.com/TecharoHQ/gubal/chromesweep package.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/TecharoHQ/gubal/chromesweep"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	cfg := chromesweep.DefaultConfig()
	kubeconfig := flag.String("kubeconfig", defaultKubeconfig(), "path to kubeconfig")
	flag.StringVar(&cfg.Namespace, "namespace", cfg.Namespace, "namespace holding the chrome resources and smoke Job")
	flag.StringVar(&cfg.Deployment, "deployment", cfg.Deployment, "base name for per-version chrome resources (Deployment/Service/NetworkPolicy)")
	flag.StringVar(&cfg.Container, "container", cfg.Container, "container within the Deployment to re-image")
	flag.StringVar(&cfg.ImageRepo, "image-repo", cfg.ImageRepo, "image repository; final ref is <repo>:<tag>")
	flag.StringVar(&cfg.JobManifest, "job-manifest", cfg.JobManifest, "path to the smoke Job manifest to run each version")
	flag.StringVar(&cfg.JobName, "job-name", cfg.JobName, "base name for per-version smoke Jobs; per version appends -<tag>")
	flag.StringVar(&cfg.DeploymentManifest, "deployment-manifest", cfg.DeploymentManifest, "base Deployment manifest to template per version")
	flag.StringVar(&cfg.ServiceManifest, "service-manifest", cfg.ServiceManifest, "base Service manifest to template per version")
	flag.StringVar(&cfg.NetworkPolicyManifest, "networkpolicy-manifest", cfg.NetworkPolicyManifest, "base NetworkPolicy manifest to template per version")
	flag.StringVar(&cfg.AnubisManifest, "anubis-manifest", cfg.AnubisManifest, "Anubis Deployment manifest; the tested Anubis image ref is read from it")
	flag.StringVar(&cfg.AnubisContainer, "anubis-container", cfg.AnubisContainer, "container in the Anubis Deployment that holds the Anubis image")
	flag.StringVar(&cfg.AnubisImage, "anubis-image", cfg.AnubisImage, "override the Anubis image ref (default: the ref from -anubis-manifest); when set, the live Anubis Deployment is re-imaged for the run and restored after")
	flag.IntVar(&cfg.Parallelism, "parallelism", cfg.Parallelism, "max number of versions tested concurrently")
	flag.StringVar(&cfg.CollectorPVC, "pvc", cfg.CollectorPVC, "PVC that holds captured frames")
	flag.StringVar(&cfg.OutDir, "out", cfg.OutDir, "directory to write the report and copied frames into")
	flag.DurationVar(&cfg.ReadyTimeout, "ready-timeout", cfg.ReadyTimeout, "max wait for a version's rollout to become Ready")
	flag.DurationVar(&cfg.JobTimeout, "job-timeout", cfg.JobTimeout, "max wait for the smoke Job to finish")
	flag.Parse()

	versions, err := chromesweep.ParseVersions(flag.Args())
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

func run(kubeconfig string, cfg chromesweep.Config) error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	cs, err := kubeClient(kubeconfig)
	if err != nil {
		return err
	}
	cluster := chromesweep.NewCluster(cs, cfg.Namespace)

	ctx := context.Background()

	// Frames are copied into a scratch dir, bundled into report.zip, then removed.
	framesDir, err := os.MkdirTemp("", "chrome-sweep-frames-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(framesDir)

	rep, err := chromesweep.Run(ctx, cluster, cfg, framesDir)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return err
	}
	js, err := chromesweep.RenderJSON(rep)
	if err != nil {
		return err
	}
	md := chromesweep.RenderMarkdown(rep)
	if err := chromesweep.WriteBundle(filepath.Join(cfg.OutDir, "report.zip"), js, []byte(md), rep.Results); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(cfg.OutDir, "report.md"), []byte(md), 0o644); err != nil {
		return err
	}
	fmt.Print(md)

	if !rep.AllPassed() {
		return fmt.Errorf("one or more versions did not pass; see %s/report.zip", cfg.OutDir)
	}
	return nil
}
