package chromesweep

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
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

// SetImage strategic-merge-patches one container's image on a Deployment. Used
// for the shared, singleton Anubis Deployment (per-version chrome resources are
// created outright, not patched).
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

// ContainerImage returns the current image of the named container in a Deployment.
func (c *Cluster) ContainerImage(ctx context.Context, deployment, container string) (string, error) {
	d, err := c.cs.AppsV1().Deployments(c.ns).Get(ctx, deployment, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	for _, ct := range d.Spec.Template.Spec.Containers {
		if ct.Name == container {
			return ct.Image, nil
		}
	}
	return "", fmt.Errorf("container %q not found in deployment %s", container, deployment)
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

// waitGone polls until get reports the object is NotFound.
func waitGone(ctx context.Context, timeout time.Duration, get func(context.Context) error) error {
	return wait.PollUntilContextTimeout(ctx, time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			err := get(ctx)
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			return false, nil
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
		if werr := waitGone(ctx, 2*time.Minute, func(ctx context.Context) error {
			_, e := c.cs.BatchV1().Jobs(c.ns).Get(ctx, job.Name, metav1.GetOptions{})
			return e
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

// CreateOrReplaceDeployment deletes any existing Deployment of the same name
// (waiting for it to clear) and creates the given one. Delete+create rather than
// update because a Deployment's selector is immutable.
func (c *Cluster) CreateOrReplaceDeployment(ctx context.Context, dep *appsv1.Deployment, timeout time.Duration) error {
	dep.Namespace = c.ns
	api := c.cs.AppsV1().Deployments(c.ns)
	err := api.Delete(ctx, dep.Name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting old deployment %s: %w", dep.Name, err)
	}
	if err == nil {
		if werr := waitGone(ctx, timeout, func(ctx context.Context) error {
			_, e := api.Get(ctx, dep.Name, metav1.GetOptions{})
			return e
		}); werr != nil {
			return fmt.Errorf("waiting for old deployment %s to clear: %w", dep.Name, werr)
		}
	}
	if _, err := api.Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating deployment %s: %w", dep.Name, err)
	}
	return nil
}

// CreateOrReplaceService deletes any existing Service of the same name and creates
// the given one.
func (c *Cluster) CreateOrReplaceService(ctx context.Context, svc *corev1.Service, timeout time.Duration) error {
	svc.Namespace = c.ns
	api := c.cs.CoreV1().Services(c.ns)
	err := api.Delete(ctx, svc.Name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting old service %s: %w", svc.Name, err)
	}
	if err == nil {
		if werr := waitGone(ctx, timeout, func(ctx context.Context) error {
			_, e := api.Get(ctx, svc.Name, metav1.GetOptions{})
			return e
		}); werr != nil {
			return fmt.Errorf("waiting for old service %s to clear: %w", svc.Name, werr)
		}
	}
	if _, err := api.Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating service %s: %w", svc.Name, err)
	}
	return nil
}

// CreateOrReplaceNetworkPolicy deletes any existing NetworkPolicy of the same name
// and creates the given one.
func (c *Cluster) CreateOrReplaceNetworkPolicy(ctx context.Context, np *networkingv1.NetworkPolicy, timeout time.Duration) error {
	np.Namespace = c.ns
	api := c.cs.NetworkingV1().NetworkPolicies(c.ns)
	err := api.Delete(ctx, np.Name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting old networkpolicy %s: %w", np.Name, err)
	}
	if err == nil {
		if werr := waitGone(ctx, timeout, func(ctx context.Context) error {
			_, e := api.Get(ctx, np.Name, metav1.GetOptions{})
			return e
		}); werr != nil {
			return fmt.Errorf("waiting for old networkpolicy %s to clear: %w", np.Name, werr)
		}
	}
	if _, err := api.Create(ctx, np, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("creating networkpolicy %s: %w", np.Name, err)
	}
	return nil
}

// DeleteVersionResources removes a version's Deployment, Service, NetworkPolicy
// (<name>-lockdown), and Job (jobName). It tolerates already-absent resources and
// returns the joined error of any real failures (best-effort teardown).
func (c *Cluster) DeleteVersionResources(ctx context.Context, name, jobName string) error {
	fg := metav1.DeletePropagationForeground
	var errs []error
	if err := c.cs.AppsV1().Deployments(c.ns).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &fg}); err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, err)
	}
	if err := c.cs.CoreV1().Services(c.ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, err)
	}
	if err := c.cs.NetworkingV1().NetworkPolicies(c.ns).Delete(ctx, name+"-lockdown", metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, err)
	}
	if err := c.cs.BatchV1().Jobs(c.ns).Delete(ctx, jobName, metav1.DeleteOptions{PropagationPolicy: &fg}); err != nil && !apierrors.IsNotFound(err) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
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
	err := c.cs.CoreV1().Pods(c.ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting stale collector: %w", err)
	}
	if err == nil {
		if werr := wait.PollUntilContextTimeout(ctx, time.Second, timeout, true,
			func(ctx context.Context) (bool, error) {
				_, gerr := c.cs.CoreV1().Pods(c.ns).Get(ctx, name, metav1.GetOptions{})
				if apierrors.IsNotFound(gerr) {
					return true, nil
				}
				if gerr != nil {
					return false, gerr
				}
				return false, nil
			}); werr != nil {
			return fmt.Errorf("waiting for stale collector to clear: %w", werr)
		}
	}

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
	_, err = c.cs.CoreV1().Pods(c.ns).Create(ctx, pod, metav1.CreateOptions{})
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
