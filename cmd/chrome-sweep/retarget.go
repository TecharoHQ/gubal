package main

import (
	"fmt"
	"os"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// versionedName joins a base resource name and a version tag: chrome + 150 -> chrome-150.
func versionedName(base, tag string) string {
	return base + "-" + tag
}

// retargetDeployment renames the Deployment and repoints its selector/pod labels
// to name, and sets the named container's image.
func retargetDeployment(dep *appsv1.Deployment, name, container, image string) {
	dep.Name = name
	if dep.Labels == nil {
		dep.Labels = map[string]string{}
	}
	dep.Labels["app"] = name
	if dep.Spec.Selector == nil {
		dep.Spec.Selector = &metav1.LabelSelector{}
	}
	if dep.Spec.Selector.MatchLabels == nil {
		dep.Spec.Selector.MatchLabels = map[string]string{}
	}
	dep.Spec.Selector.MatchLabels["app"] = name
	if dep.Spec.Template.Labels == nil {
		dep.Spec.Template.Labels = map[string]string{}
	}
	dep.Spec.Template.Labels["app"] = name
	for i := range dep.Spec.Template.Spec.Containers {
		if dep.Spec.Template.Spec.Containers[i].Name == container {
			dep.Spec.Template.Spec.Containers[i].Image = image
		}
	}
}

// retargetService renames the Service and repoints its selector to name.
func retargetService(svc *corev1.Service, name string) {
	svc.Name = name
	if svc.Labels == nil {
		svc.Labels = map[string]string{}
	}
	svc.Labels["app"] = name
	if svc.Spec.Selector == nil {
		svc.Spec.Selector = map[string]string{}
	}
	svc.Spec.Selector["app"] = name
}

// retargetNetworkPolicy renames the policy to <name>-lockdown and repoints its
// podSelector to name.
func retargetNetworkPolicy(np *networkingv1.NetworkPolicy, name string) {
	np.Name = name + "-lockdown"
	if np.Spec.PodSelector.MatchLabels == nil {
		np.Spec.PodSelector.MatchLabels = map[string]string{}
	}
	np.Spec.PodSelector.MatchLabels["app"] = name
}

// retargetJob renames the Job and rewrites the CDP host in every container arg
// from baseHost:9222 to versionedHost:9222 (leaving Host: localhost:9222 alone).
func retargetJob(job *batchv1.Job, name, baseHost, versionedHost string) {
	job.Name = name
	old := baseHost + ":9222"
	replacement := versionedHost + ":9222"
	cs := job.Spec.Template.Spec.Containers
	for i := range cs {
		for j := range cs[i].Args {
			cs[i].Args[j] = strings.ReplaceAll(cs[i].Args[j], old, replacement)
		}
	}
}

func loadDeployment(path, namespace string) (*appsv1.Deployment, error) {
	var d appsv1.Deployment
	if err := decodeManifest(path, &d); err != nil {
		return nil, err
	}
	d.Namespace = namespace
	return &d, nil
}

func loadService(path, namespace string) (*corev1.Service, error) {
	var s corev1.Service
	if err := decodeManifest(path, &s); err != nil {
		return nil, err
	}
	s.Namespace = namespace
	return &s, nil
}

func loadNetworkPolicy(path, namespace string) (*networkingv1.NetworkPolicy, error) {
	var np networkingv1.NetworkPolicy
	if err := decodeManifest(path, &np); err != nil {
		return nil, err
	}
	np.Namespace = namespace
	return &np, nil
}

func decodeManifest(path string, into any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("decoding %s: %w", path, err)
	}
	return nil
}
