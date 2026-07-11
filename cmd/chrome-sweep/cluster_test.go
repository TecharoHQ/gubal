package main

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestSetImage(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "chrome", Namespace: "ci"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "chrome", Image: "old:1"}}},
			},
		},
	}
	cs := fake.NewSimpleClientset(dep)
	c := NewCluster(cs, "ci")
	if err := c.SetImage(context.Background(), "chrome", "chrome", "repo:120"); err != nil {
		t.Fatal(err)
	}
	got, _ := cs.AppsV1().Deployments("ci").Get(context.Background(), "chrome", metav1.GetOptions{})
	if img := got.Spec.Template.Spec.Containers[0].Image; img != "repo:120" {
		t.Fatalf("image = %q, want repo:120", img)
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
