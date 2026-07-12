// Package chromesweep tests lists of browser (Chrome and Firefox) image tags
// against an in-cluster Anubis setup: for each browser it stands up per-version
// Deployment/Service/NetworkPolicy and a smoke Job, runs it, records pass/fail plus
// a captured screenshot, then tears the version's resources down. Anubis and the
// frame collector are set up once per run and shared across browsers. It is
// importable so services (not just the CLI) can drive a sweep.
package chromesweep

import "time"

// Browser describes one browser target to sweep: its image repo, the base names
// for its per-version resources, the manifests to template, and the versions.
type Browser struct {
	Name                  string // "chrome" / "firefox": report section, frame prefix, resource base
	ImageRepo             string // final image ref is <ImageRepo>:<tag>
	Deployment            string // base name -> <name>-<tag> resources; also the CDP host in the Job
	Container             string // container within the Deployment to re-image
	JobName               string // base name for per-version smoke Jobs
	DeploymentManifest    string
	ServiceManifest       string
	NetworkPolicyManifest string
	JobManifest           string
	Versions              []string
}

// ChromeBrowser returns the Chrome target preset (base k8s/*.yaml manifests).
func ChromeBrowser() Browser {
	return Browser{
		Name:                  "chrome",
		ImageRepo:             "ghcr.io/techarohq/gubal/chrome",
		Deployment:            "chrome",
		Container:             "chrome",
		JobName:               "chrome-smoke",
		DeploymentManifest:    "k8s/deployment.yaml",
		ServiceManifest:       "k8s/service.yaml",
		NetworkPolicyManifest: "k8s/networkpolicy.yaml",
		JobManifest:           "k8s/smoke-job.yaml",
		Versions:              []string{"75", "80", "85", "90", "95", "100", "105", "110", "115", "120", "125", "130", "135", "140", "145", "150"},
	}
}

// FirefoxBrowser returns the Firefox target preset (k8s/firefox/*.yaml manifests).
func FirefoxBrowser() Browser {
	return Browser{
		Name:                  "firefox",
		ImageRepo:             "ghcr.io/techarohq/gubal/firefox",
		Deployment:            "firefox",
		Container:             "firefox",
		JobName:               "firefox-smoke",
		DeploymentManifest:    "k8s/firefox/deployment.yaml",
		ServiceManifest:       "k8s/firefox/service.yaml",
		NetworkPolicyManifest: "k8s/firefox/networkpolicy.yaml",
		JobManifest:           "k8s/firefox/smoke-job.yaml",
		Versions:              []string{"129", "135", "140", "145", "150", "152"},
	}
}

// Config is the fully-resolved run-wide configuration for a sweep. Per-browser
// target details live on each Browser in Browsers.
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
}

// DefaultConfig returns a Config with the standard defaults filled in, sweeping
// both Chrome and Firefox with their preset version lists. Callers usually set
// AnubisImage and override Browsers (and each browser's Versions).
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
	}
}
