package chromesweep

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

type runtimeObject = runtime.Object

func TestSetImage(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "ci"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "old:1"}}},
			},
		},
	}
	cs := fake.NewSimpleClientset(dep)
	c := NewCluster(cs, "ci")
	if err := c.SetImage(context.Background(), "backend", "app", "repo/app:v2"); err != nil {
		t.Fatal(err)
	}
	got, _ := cs.AppsV1().Deployments("ci").Get(context.Background(), "backend", metav1.GetOptions{})
	if img := got.Spec.Template.Spec.Containers[0].Image; img != "repo/app:v2" {
		t.Fatalf("image = %q, want repo/app:v2", img)
	}
}

func TestContainerImage(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "ci"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "repo/app:orig"}}},
			},
		},
	}
	cs := fake.NewSimpleClientset(dep)
	c := NewCluster(cs, "ci")
	got, err := c.ContainerImage(context.Background(), "backend", "app")
	if err != nil || got != "repo/app:orig" {
		t.Fatalf("ContainerImage = %q, %v", got, err)
	}
	if _, err := c.ContainerImage(context.Background(), "backend", "nope"); err == nil {
		t.Fatal("expected error for missing container")
	}
}

func TestReplaceJobCreatesWhenAbsent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	c := NewCluster(cs, "ci")
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "chrome-smoke", Namespace: "ci"}}
	if err := c.ReplaceJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.BatchV1().Jobs("ci").Get(context.Background(), "chrome-smoke", metav1.GetOptions{}); err != nil {
		t.Fatalf("job not created: %v", err)
	}
}

func TestCreateOrReplaceDeploymentCreatesWhenAbsent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	c := NewCluster(cs, "ci")
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "chrome-150"}}
	if err := c.CreateOrReplaceDeployment(context.Background(), dep, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.AppsV1().Deployments("ci").Get(context.Background(), "chrome-150", metav1.GetOptions{}); err != nil {
		t.Fatalf("deployment not created: %v", err)
	}
}

func TestCreateOrReplaceServiceReplacesExisting(t *testing.T) {
	existing := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "chrome-150", Namespace: "ci"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "old"}},
	}
	cs := fake.NewSimpleClientset(existing)
	c := NewCluster(cs, "ci")
	updated := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "chrome-150"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "chrome-150"}},
	}
	if err := c.CreateOrReplaceService(context.Background(), updated, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, err := cs.CoreV1().Services("ci").Get(context.Background(), "chrome-150", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Selector["app"] != "chrome-150" {
		t.Fatalf("selector app = %q, want chrome-150", got.Spec.Selector["app"])
	}
}

func TestDeleteVersionResourcesRemovesSet(t *testing.T) {
	objs := []runtimeObject{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "chrome-150", Namespace: "ci"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "chrome-150", Namespace: "ci"}},
		&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "chrome-150-lockdown", Namespace: "ci"}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "chrome-smoke-150", Namespace: "ci"}},
	}
	cs := fake.NewSimpleClientset(objs...)
	c := NewCluster(cs, "ci")
	if err := c.DeleteVersionResources(context.Background(), "chrome-150", "chrome-smoke-150"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := cs.AppsV1().Deployments("ci").Get(context.Background(), "chrome-150", metav1.GetOptions{}); err == nil {
		t.Fatal("deployment still present")
	}
	if _, err := cs.NetworkingV1().NetworkPolicies("ci").Get(context.Background(), "chrome-150-lockdown", metav1.GetOptions{}); err == nil {
		t.Fatal("networkpolicy still present")
	}
	// Calling again with everything absent must be a no-op (tolerate NotFound).
	if err := c.DeleteVersionResources(context.Background(), "chrome-150", "chrome-smoke-150"); err != nil {
		t.Fatalf("second delete should tolerate NotFound: %v", err)
	}
}

func TestCreateOrReplaceConfigMap(t *testing.T) {
	cs := fake.NewSimpleClientset()
	c := NewCluster(cs, "ci")
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "anubis-policy-default"},
		Data:       map[string]string{"botPolicies.yaml": "bots: []"},
	}
	if err := c.CreateOrReplaceConfigMap(context.Background(), cm); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Replace with new content: no error, content updated in place.
	cm2 := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "anubis-policy-default"},
		Data:       map[string]string{"botPolicies.yaml": "bots: [changed]"},
	}
	if err := c.CreateOrReplaceConfigMap(context.Background(), cm2); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err := cs.CoreV1().ConfigMaps("ci").Get(context.Background(), "anubis-policy-default", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Data["botPolicies.yaml"] != "bots: [changed]" {
		t.Fatalf("content = %q, want replaced", got.Data["botPolicies.yaml"])
	}
}

func TestSetAnubisPolicyAndRestore(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "anubis", Namespace: "ci"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "anubis", Image: "anubis:orig", Env: []corev1.EnvVar{{Name: "BIND", Value: ":8080"}}},
					},
				},
			},
		},
	}
	cs := fake.NewSimpleClientset(dep)
	c := NewCluster(cs, "ci")

	snap, err := c.SnapshotPodTemplate(context.Background(), "anubis")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	if err := c.SetAnubisPolicy(context.Background(), "anubis", "anubis", "anubis-policy-default"); err != nil {
		t.Fatalf("SetAnubisPolicy: %v", err)
	}
	got, _ := cs.AppsV1().Deployments("ci").Get(context.Background(), "anubis", metav1.GetOptions{})
	ct := got.Spec.Template.Spec.Containers[0]

	var envVal string
	for _, e := range ct.Env {
		if e.Name == "POLICY_FNAME" {
			envVal = e.Value
		}
	}
	if envVal != "/policy/botPolicies.yaml" {
		t.Fatalf("POLICY_FNAME = %q, want /policy/botPolicies.yaml", envVal)
	}
	if len(ct.VolumeMounts) != 1 || ct.VolumeMounts[0].Name != "anubis-policy" || ct.VolumeMounts[0].MountPath != "/policy" {
		t.Fatalf("volumeMount = %+v", ct.VolumeMounts)
	}
	vols := got.Spec.Template.Spec.Volumes
	if len(vols) != 1 || vols[0].Name != "anubis-policy" || vols[0].ConfigMap == nil || vols[0].ConfigMap.Name != "anubis-policy-default" {
		t.Fatalf("volumes = %+v", vols)
	}
	// The pre-existing env var must survive.
	found := false
	for _, e := range ct.Env {
		if e.Name == "BIND" {
			found = true
		}
	}
	if !found {
		t.Fatal("existing BIND env var was dropped")
	}

	// Re-applying a different policy must not duplicate volume/mount/env, only swap the CM name.
	if err := c.SetAnubisPolicy(context.Background(), "anubis", "anubis", "anubis-policy-hard"); err != nil {
		t.Fatalf("SetAnubisPolicy #2: %v", err)
	}
	got, _ = cs.AppsV1().Deployments("ci").Get(context.Background(), "anubis", metav1.GetOptions{})
	if v := got.Spec.Template.Spec.Volumes; len(v) != 1 || v[0].ConfigMap.Name != "anubis-policy-hard" {
		t.Fatalf("volumes after re-apply = %+v", v)
	}
	if m := got.Spec.Template.Spec.Containers[0].VolumeMounts; len(m) != 1 {
		t.Fatalf("volumeMounts duplicated: %+v", m)
	}

	// Restore returns the template to the original (no POLICY_FNAME, no policy volume).
	if err := c.RestorePodTemplate(context.Background(), "anubis", snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got, _ = cs.AppsV1().Deployments("ci").Get(context.Background(), "anubis", metav1.GetOptions{})
	if len(got.Spec.Template.Spec.Volumes) != 0 {
		t.Fatalf("restore left volumes: %+v", got.Spec.Template.Spec.Volumes)
	}
	for _, e := range got.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "POLICY_FNAME" {
			t.Fatal("restore left POLICY_FNAME set")
		}
	}
}
