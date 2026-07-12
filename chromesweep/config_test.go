package chromesweep

import (
	"strings"
	"testing"
)

func TestBrowserPresets(t *testing.T) {
	c := ChromeBrowser()
	if c.Name != "chrome" || c.Container != "chrome" || c.Deployment != "chrome" || c.JobName != "chrome-smoke" {
		t.Fatalf("chrome preset names wrong: %+v", c)
	}
	if c.ImageRepo != "ghcr.io/techarohq/gubal/chrome" {
		t.Fatalf("chrome repo = %q", c.ImageRepo)
	}
	if c.DeploymentManifest != "k8s/deployment.yaml" || c.JobManifest != "k8s/smoke-job.yaml" {
		t.Fatalf("chrome manifests wrong: %+v", c)
	}
	if strings.Join(c.Versions, ",") != "75,80,85,90,95,100,105,110,115,120,125,130,135,140,145,150" {
		t.Fatalf("chrome default versions = %v", c.Versions)
	}

	f := FirefoxBrowser()
	if f.Name != "firefox" || f.Container != "firefox" || f.Deployment != "firefox" || f.JobName != "firefox-smoke" {
		t.Fatalf("firefox preset names wrong: %+v", f)
	}
	if f.ImageRepo != "ghcr.io/techarohq/gubal/firefox" {
		t.Fatalf("firefox repo = %q", f.ImageRepo)
	}
	if f.DeploymentManifest != "k8s/firefox/deployment.yaml" || f.JobManifest != "k8s/firefox/smoke-job.yaml" {
		t.Fatalf("firefox manifests wrong: %+v", f)
	}
	if strings.Join(f.Versions, ",") != "146,147,148,149,150,151,152" {
		t.Fatalf("firefox default versions = %v", f.Versions)
	}
}

func TestDefaultConfigSweepsBothBrowsers(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Namespace != "ci" || cfg.Parallelism != 8 || cfg.CollectorPVC != "chrome-bully-data" {
		t.Fatalf("run-wide defaults wrong: %+v", cfg)
	}
	if len(cfg.Browsers) != 2 || cfg.Browsers[0].Name != "chrome" || cfg.Browsers[1].Name != "firefox" {
		t.Fatalf("DefaultConfig browsers = %+v", cfg.Browsers)
	}
}

func TestDefaultConfigLoadsPolicies(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Policies) < 2 {
		t.Fatalf("DefaultConfig should carry the embedded policies, got %d", len(cfg.Policies))
	}
	seen := map[string]bool{}
	for _, p := range cfg.Policies {
		seen[p.Name] = true
	}
	if !seen["default-config"] {
		t.Fatalf(`DefaultConfig missing the "default-config" policy; have %v`, seen)
	}
}

// TestLoadFirefoxManifests decodes the real k8s/firefox/*.yaml so a malformed
// manifest fails here rather than at cluster time.
func TestLoadFirefoxManifests(t *testing.T) {
	if _, err := loadDeployment("../k8s/firefox/deployment.yaml", "ci"); err != nil {
		t.Fatalf("firefox deployment: %v", err)
	}
	if _, err := loadService("../k8s/firefox/service.yaml", "ci"); err != nil {
		t.Fatalf("firefox service: %v", err)
	}
	if _, err := loadNetworkPolicy("../k8s/firefox/networkpolicy.yaml", "ci"); err != nil {
		t.Fatalf("firefox networkpolicy: %v", err)
	}
	if _, err := loadJob("../k8s/firefox/smoke-job.yaml", "ci"); err != nil {
		t.Fatalf("firefox job: %v", err)
	}
}
