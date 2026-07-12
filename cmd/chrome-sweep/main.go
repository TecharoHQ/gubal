// Command chrome-sweep tests a list of archived Chrome and Firefox image tags
// in bounded parallel: for each tag it creates a per-version browser
// Deployment/Service/NetworkPolicy and smoke Job (named <browser>-<tag>), waits
// for rollout, runs the smoke Job, records a pass/fail + screenshot, then tears
// the version's resources down. It is a thin CLI over the importable
// github.com/TecharoHQ/gubal/chromesweep package.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/TecharoHQ/gubal/chromesweep"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	cfg := chromesweep.DefaultConfig()
	kubeconfig := flag.String("kubeconfig", defaultKubeconfig(), "path to kubeconfig")
	flag.StringVar(&cfg.Namespace, "namespace", cfg.Namespace, "namespace holding the browser resources and smoke Jobs")
	flag.StringVar(&cfg.AnubisManifest, "anubis-manifest", cfg.AnubisManifest, "Anubis Deployment manifest; the tested Anubis image ref is read from it")
	flag.StringVar(&cfg.AnubisContainer, "anubis-container", cfg.AnubisContainer, "container in the Anubis Deployment that holds the Anubis image")
	flag.StringVar(&cfg.AnubisImage, "anubis-image", cfg.AnubisImage, "override the Anubis image ref (default: the ref from -anubis-manifest); when set, the live Anubis Deployment is re-imaged for the run and restored after")
	flag.IntVar(&cfg.Parallelism, "parallelism", cfg.Parallelism, "max number of versions tested concurrently")
	flag.StringVar(&cfg.CollectorPVC, "pvc", cfg.CollectorPVC, "PVC that holds captured frames")
	flag.StringVar(&cfg.OutDir, "out", cfg.OutDir, "directory to write the report and copied frames into")
	flag.DurationVar(&cfg.ReadyTimeout, "ready-timeout", cfg.ReadyTimeout, "max wait for a version's rollout to become Ready")
	flag.DurationVar(&cfg.JobTimeout, "job-timeout", cfg.JobTimeout, "max wait for a smoke Job to finish")

	// Default to the preset version lists; either flag overrides its browser.
	chromeVersions := flag.String("chrome-versions", strings.Join(chromesweep.ChromeBrowser().Versions, ","), "comma-separated Chrome major versions to sweep (empty to skip Chrome)")
	firefoxVersions := flag.String("firefox-versions", strings.Join(chromesweep.FirefoxBrowser().Versions, ","), "comma-separated Firefox major versions to sweep (empty to skip Firefox)")
	flag.Parse()

	browsers, err := browsersFromFlags(*chromeVersions, *firefoxVersions)
	if err != nil {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
		slog.Error("bad versions", "err", err)
		os.Exit(1)
	}
	cfg.Browsers = browsers

	if err := run(*kubeconfig, cfg); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// browsersFromFlags builds the browser targets from comma-separated version
// lists. An empty list skips that browser; at least one browser must survive.
func browsersFromFlags(chromeCSV, firefoxCSV string) ([]chromesweep.Browser, error) {
	var browsers []chromesweep.Browser
	for _, spec := range []struct {
		csv     string
		browser chromesweep.Browser
	}{
		{chromeCSV, chromesweep.ChromeBrowser()},
		{firefoxCSV, chromesweep.FirefoxBrowser()},
	} {
		fields := strings.Split(spec.csv, ",")
		vs, err := chromesweep.ParseVersions(fields)
		if err != nil {
			// Empty list -> skip this browser rather than failing the run.
			if strings.TrimSpace(spec.csv) == "" {
				continue
			}
			return nil, fmt.Errorf("%s: %w", spec.browser.Name, err)
		}
		b := spec.browser
		b.Versions = vs
		browsers = append(browsers, b)
	}
	if len(browsers) == 0 {
		return nil, fmt.Errorf("no versions given for any browser")
	}
	return browsers, nil
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
