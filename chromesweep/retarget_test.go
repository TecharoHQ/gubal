package chromesweep

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestVersionedName(t *testing.T) {
	if got := versionedName("chrome", "150"); got != "chrome-150" {
		t.Fatalf("got %q, want chrome-150", got)
	}
	if got := versionedName("chrome-smoke", "150"); got != "chrome-smoke-150" {
		t.Fatalf("got %q, want chrome-smoke-150", got)
	}
}

func TestRetargetDeployment(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "chrome", Labels: map[string]string{"app": "chrome"}},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "chrome"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "chrome"}},
				Spec: corev1.PodSpec{Containers: []corev1.Container{
					{Name: "chrome", Image: "chrome"},
					{Name: "sidecar", Image: "sidecar:1"},
				}},
			},
		},
	}
	retargetDeployment(dep, "chrome-150", "chrome", "repo/chrome:150")
	if dep.Name != "chrome-150" {
		t.Fatalf("name = %q", dep.Name)
	}
	if dep.Spec.Selector.MatchLabels["app"] != "chrome-150" {
		t.Fatalf("selector app = %q", dep.Spec.Selector.MatchLabels["app"])
	}
	if dep.Spec.Template.Labels["app"] != "chrome-150" {
		t.Fatalf("template app = %q", dep.Spec.Template.Labels["app"])
	}
	if dep.Spec.Template.Spec.Containers[0].Image != "repo/chrome:150" {
		t.Fatalf("chrome image = %q", dep.Spec.Template.Spec.Containers[0].Image)
	}
	if dep.Spec.Template.Spec.Containers[1].Image != "sidecar:1" {
		t.Fatalf("sidecar image should be untouched, got %q", dep.Spec.Template.Spec.Containers[1].Image)
	}
}

func TestRetargetService(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "chrome"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "chrome"}},
	}
	retargetService(svc, "chrome-150")
	if svc.Name != "chrome-150" || svc.Spec.Selector["app"] != "chrome-150" {
		t.Fatalf("svc = %q / %q", svc.Name, svc.Spec.Selector["app"])
	}
}

func TestRetargetNetworkPolicy(t *testing.T) {
	np := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "chrome-lockdown"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "chrome"}},
		},
	}
	retargetNetworkPolicy(np, "chrome-150")
	if np.Name != "chrome-150-lockdown" {
		t.Fatalf("np name = %q", np.Name)
	}
	if np.Spec.PodSelector.MatchLabels["app"] != "chrome-150" {
		t.Fatalf("np podSelector app = %q", np.Spec.PodSelector.MatchLabels["app"])
	}
}

func TestRetargetJob(t *testing.T) {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "chrome-smoke"},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name: "smoke",
					Args: []string{
						"echo waiting for chrome:9222; curl -H 'Host: localhost:9222' http://chrome:9222/json/version",
					},
				},
				{
					Name: "chrome-bully",
					Args: []string{"-cdp-url=http://chrome:9222", "-target-url=https://example"},
				},
			},
		}}},
	}
	retargetJob(job, "chrome-smoke-150", "chrome", "chrome-150")
	if job.Name != "chrome-smoke-150" {
		t.Fatalf("job name = %q", job.Name)
	}
	smoke := job.Spec.Template.Spec.Containers[0].Args[0]
	if want := "http://chrome-150:9222/json/version"; !strings.Contains(smoke, want) {
		t.Fatalf("smoke arg not retargeted: %q", smoke)
	}
	if !strings.Contains(smoke, "Host: localhost:9222") {
		t.Fatalf("localhost host header must be preserved: %q", smoke)
	}
	if got := job.Spec.Template.Spec.Containers[1].Args[0]; got != "-cdp-url=http://chrome-150:9222" {
		t.Fatalf("chrome-bully cdp arg = %q", got)
	}
}

func TestRetargetJobFirefox(t *testing.T) {
	job := &batchv1.Job{}
	job.Spec.Template.Spec.Containers = []corev1.Container{{
		Args: []string{"-cdp-url=http://firefox:9222", "-header=Host: localhost:9222"},
	}}
	retargetJob(job, "firefox-smoke-152", "firefox", "firefox-152")
	got := job.Spec.Template.Spec.Containers[0].Args
	if got[0] != "-cdp-url=http://firefox-152:9222" {
		t.Fatalf("cdp url not rewritten: %q", got[0])
	}
	if got[1] != "-header=Host: localhost:9222" {
		t.Fatalf("localhost host must be untouched: %q", got[1])
	}
}

func TestLoadManifests(t *testing.T) {
	dep, err := loadDeployment("../k8s/deployment.yaml", "ci")
	if err != nil || dep.Name != "chrome" || dep.Namespace != "ci" {
		t.Fatalf("loadDeployment: %v name=%q ns=%q", err, dep.GetName(), dep.GetNamespace())
	}
	svc, err := loadService("../k8s/service.yaml", "ci")
	if err != nil || svc.Name != "chrome" {
		t.Fatalf("loadService: %v name=%q", err, svc.GetName())
	}
	np, err := loadNetworkPolicy("../k8s/networkpolicy.yaml", "ci")
	if err != nil || np.Name != "chrome-lockdown" {
		t.Fatalf("loadNetworkPolicy: %v name=%q", err, np.GetName())
	}
}

// The Anubis manifest is a multi-document YAML (Deployment then Service). Confirm
// loadDeployment reads the Deployment (first doc) and that the anubis container's
// image ref is recoverable from it — that ref is what prepareAnubis reports.
func TestLoadAnubisManifestImage(t *testing.T) {
	dep, err := loadDeployment("../k8s/anubis/anubis.yaml", "ci")
	if err != nil {
		t.Fatalf("loadDeployment(anubis): %v", err)
	}
	if dep.Name != "anubis" {
		t.Fatalf("anubis deployment name = %q, want anubis", dep.Name)
	}
	image := ""
	for _, ct := range dep.Spec.Template.Spec.Containers {
		if ct.Name == "anubis" {
			image = ct.Image
		}
	}
	if image == "" {
		t.Fatalf("anubis container image not found in manifest; containers=%d", len(dep.Spec.Template.Spec.Containers))
	}
}
