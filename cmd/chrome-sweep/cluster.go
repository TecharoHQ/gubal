package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// Cluster wraps a clientset scoped to one namespace.
type Cluster struct {
	cs kubernetes.Interface
	ns string
}

func NewCluster(cs kubernetes.Interface, namespace string) *Cluster {
	return &Cluster{cs: cs, ns: namespace}
}

// SetImage strategic-merge-patches one container's image on a Deployment.
func (c *Cluster) SetImage(ctx context.Context, deployment, container, image string) error {
	patch := []byte(fmt.Sprintf(
		`{"spec":{"template":{"spec":{"containers":[{"name":%q,"image":%q}]}}}}`, container, image))
	_, err := c.cs.AppsV1().Deployments(c.ns).Patch(
		ctx, deployment, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patching %s image: %w", deployment, err)
	}
	return nil
}

// WaitDeploymentReady blocks until the Deployment's newest generation is fully
// rolled out and available, mirroring `kubectl rollout status`.
func (c *Cluster) WaitDeploymentReady(ctx context.Context, deployment string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			d, err := c.cs.AppsV1().Deployments(c.ns).Get(ctx, deployment, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			desired := int32(1)
			if d.Spec.Replicas != nil {
				desired = *d.Spec.Replicas
			}
			s := d.Status
			ready := d.Generation == s.ObservedGeneration &&
				s.UpdatedReplicas == desired &&
				s.AvailableReplicas == desired &&
				s.UnavailableReplicas == 0
			return ready, nil
		})
}

// ReplaceJob deletes any existing Job of the same name (waiting for it to fully
// disappear) and creates the given one.
func (c *Cluster) ReplaceJob(ctx context.Context, job *batchv1.Job) error {
	fg := metav1.DeletePropagationForeground
	err := c.cs.BatchV1().Jobs(c.ns).Delete(ctx, job.Name, metav1.DeleteOptions{PropagationPolicy: &fg})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting old job: %w", err)
	}
	if err == nil {
		if werr := wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true,
			func(ctx context.Context) (bool, error) {
				_, gerr := c.cs.BatchV1().Jobs(c.ns).Get(ctx, job.Name, metav1.GetOptions{})
				if apierrors.IsNotFound(gerr) {
					return true, nil
				}
				if gerr != nil {
					return false, gerr
				}
				return false, nil
			}); werr != nil {
			return fmt.Errorf("waiting for old job to clear: %w", werr)
		}
	}
	job.Namespace = c.ns
	if _, err := c.cs.BatchV1().Jobs(c.ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating job: %w", err)
	}
	return nil
}

// WaitJob blocks until the Job reports Complete (succeeded=true) or Failed
// (succeeded=false). A timeout is returned as an error.
func (c *Cluster) WaitJob(ctx context.Context, name string, timeout time.Duration) (bool, error) {
	succeeded := false
	err := wait.PollUntilContextTimeout(ctx, 3*time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			j, err := c.cs.BatchV1().Jobs(c.ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			for _, cond := range j.Status.Conditions {
				if cond.Status != corev1.ConditionTrue {
					continue
				}
				switch cond.Type {
				case batchv1.JobComplete:
					succeeded = true
					return true, nil
				case batchv1.JobFailed:
					succeeded = false
					return true, nil
				}
			}
			return false, nil
		})
	return succeeded, err
}

// JobContainerLogs returns the logs of the named container in the newest pod
// belonging to the Job.
func (c *Cluster) JobContainerLogs(ctx context.Context, jobName, container string) (string, error) {
	pods, err := c.cs.CoreV1().Pods(c.ns).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + jobName,
	})
	if err != nil {
		return "", err
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods for job %s", jobName)
	}
	newest := pods.Items[0]
	for _, p := range pods.Items[1:] {
		if p.CreationTimestamp.After(newest.CreationTimestamp.Time) {
			newest = p
		}
	}
	req := c.cs.CoreV1().Pods(c.ns).GetLogs(newest.Name, &corev1.PodLogOptions{Container: container})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	b, err := io.ReadAll(stream)
	return string(b), err
}

// loadJob reads a Job manifest from disk and pins its namespace.
func loadJob(path, namespace string) (*batchv1.Job, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var job batchv1.Job
	if err := yaml.Unmarshal(raw, &job); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", path, err)
	}
	job.Namespace = namespace
	return &job, nil
}

// EnsureCollector creates (idempotently) a long-lived busybox pod that mounts the
// frames PVC read-only, so CopyFrame can pull files off it, and waits for Running.
func (c *Cluster) EnsureCollector(ctx context.Context, name, pvc string, timeout time.Duration) error {
	ro := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: c.ns},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "collector",
				Image:   "busybox:1.36",
				Command: []string{"sleep", "infinity"},
				VolumeMounts: []corev1.VolumeMount{{
					Name: "frames", MountPath: "/data", ReadOnly: true,
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "frames",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvc, ReadOnly: ro,
					},
				},
			}},
		},
	}
	_, err := c.cs.CoreV1().Pods(c.ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating collector: %w", err)
	}
	return wait.PollUntilContextTimeout(ctx, time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			p, err := c.cs.CoreV1().Pods(c.ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			return p.Status.Phase == corev1.PodRunning, nil
		})
}

// DeleteCollector removes the collector pod.
func (c *Cluster) DeleteCollector(ctx context.Context, name string) error {
	err := c.cs.CoreV1().Pods(c.ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// CopyFrame copies remotePath out of the collector pod to localPath using
// `kubectl cp` (which needs `tar` in the pod — busybox provides it).
func (c *Cluster) CopyFrame(ctx context.Context, collector, remotePath, localPath string) error {
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return err
	}
	src := fmt.Sprintf("%s/%s:%s", c.ns, collector, remotePath)
	cmd := exec.CommandContext(ctx, "kubectl", "cp", src, localPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("kubectl cp %s: %w: %s", src, err, out)
	}
	return nil
}
