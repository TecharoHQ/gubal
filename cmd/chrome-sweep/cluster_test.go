package main

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
