// Package chromesweep tests a list of Chrome image tags against an in-cluster
// Anubis setup: for each tag it creates a per-version chrome
// Deployment/Service/NetworkPolicy and smoke Job, runs it, records pass/fail plus
// a captured screenshot, then tears the version's resources down. It is importable
// so services (not just the CLI) can drive a sweep.
package chromesweep

import "time"

// Config is the fully-resolved run configuration for a sweep.
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

// DefaultConfig returns a Config with the standard defaults filled in. Callers set
// Versions (and usually AnubisImage) and override anything environment-specific.
func DefaultConfig() Config {
	return Config{
		Namespace:             "ci",
		Deployment:            "chrome",
		Container:             "chrome",
		ImageRepo:             "ghcr.io/techarohq/gubal/chrome",
		JobManifest:           "k8s/smoke-job.yaml",
		JobName:               "chrome-smoke",
		DeploymentManifest:    "k8s/deployment.yaml",
		ServiceManifest:       "k8s/service.yaml",
		NetworkPolicyManifest: "k8s/networkpolicy.yaml",
		AnubisManifest:        "k8s/anubis/anubis.yaml",
		AnubisContainer:       "anubis",
		Parallelism:           8,
		CollectorPVC:          "chrome-bully-data",
		OutDir:                "./var/sweep",
		ReadyTimeout:          3 * time.Minute,
		JobTimeout:            4 * time.Minute,
	}
}
