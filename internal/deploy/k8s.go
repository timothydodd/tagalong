package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/timothydodd/tagalong/internal/model"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// ErrNoCluster is returned by k8s operations when no cluster is configured. This
// lets the service boot for local UI/API development without a reachable
// cluster; actual deploys report this error instead of crashing.
var ErrNoCluster = errors.New("kubernetes client not configured (set TAGALONG_KUBECONFIG or run in-cluster)")

// K8s wraps a Kubernetes clientset with the operations tagalong needs. A nil
// clientset means no cluster is configured (degraded local-dev mode).
type K8s struct {
	cs kubernetes.Interface
}

// NewK8s builds a Kubernetes client. If kubeconfig is non-empty it is used
// (local dev); otherwise in-cluster config is used. On failure it returns a
// degraded client (Configured()==false) plus the error, so the caller can log a
// warning and still serve the UI/API.
func NewK8s(kubeconfig string) (*K8s, error) {
	var cfg *rest.Config
	var err error
	if kubeconfig != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		cfg, err = rest.InClusterConfig()
	}
	if err != nil {
		return &K8s{}, fmt.Errorf("build kube config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return &K8s{}, err
	}
	return &K8s{cs: cs}, nil
}

// NewK8sWithClient wraps an existing clientset (used in tests with a fake).
func NewK8sWithClient(cs kubernetes.Interface) *K8s {
	return &K8s{cs: cs}
}

// Configured reports whether a usable Kubernetes client is present.
func (k *K8s) Configured() bool { return k.cs != nil }

// CurrentImage returns the image reference currently set on the target's
// container in the live workload.
func (k *K8s) CurrentImage(ctx context.Context, t model.Target) (string, error) {
	if k.cs == nil {
		return "", ErrNoCluster
	}
	containers, err := k.containers(ctx, t)
	if err != nil {
		return "", err
	}
	for _, c := range containers {
		if c.Name == t.Container {
			return c.Image, nil
		}
	}
	return "", fmt.Errorf("container %q not found in %s/%s %s", t.Container, t.Namespace, t.Name, t.Kind)
}

func (k *K8s) containers(ctx context.Context, t model.Target) ([]corev1.Container, error) {
	switch t.Kind {
	case model.KindStatefulSet:
		ss, err := k.cs.AppsV1().StatefulSets(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return ss.Spec.Template.Spec.Containers, nil
	default:
		d, err := k.cs.AppsV1().Deployments(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return d.Spec.Template.Spec.Containers, nil
	}
}

// PatchImage sets the container image on the target via a strategic-merge patch.
func (k *K8s) PatchImage(ctx context.Context, t model.Target, image string) error {
	if k.cs == nil {
		return ErrNoCluster
	}
	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []map[string]any{
						{"name": t.Container, "image": image},
					},
				},
			},
		},
	}
	return k.patch(ctx, t, patch)
}

// RestartRollout triggers a rolling restart by setting the standard
// kubectl.kubernetes.io/restartedAt annotation on the pod template.
func (k *K8s) RestartRollout(ctx context.Context, t model.Target, now time.Time) error {
	if k.cs == nil {
		return ErrNoCluster
	}
	patch := map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]any{
						"kubectl.kubernetes.io/restartedAt": now.UTC().Format(time.RFC3339),
					},
				},
			},
		},
	}
	return k.patch(ctx, t, patch)
}

func (k *K8s) patch(ctx context.Context, t model.Target, patch map[string]any) error {
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	switch t.Kind {
	case model.KindStatefulSet:
		_, err = k.cs.AppsV1().StatefulSets(t.Namespace).Patch(ctx, t.Name, types.StrategicMergePatchType, data, metav1.PatchOptions{})
	default:
		_, err = k.cs.AppsV1().Deployments(t.Namespace).Patch(ctx, t.Name, types.StrategicMergePatchType, data, metav1.PatchOptions{})
	}
	return err
}

// RolloutStatus is the observed state of a workload rollout.
type RolloutStatus struct {
	Done    bool   // rollout complete and healthy
	Failed  bool   // rollout will not progress (e.g. ProgressDeadlineExceeded)
	Message string // human-readable detail
}

// WaitForRollout polls the target until its rollout completes, fails, or ctx is
// cancelled. It reports pod-level reasons (ImagePullBackOff etc.) on failure.
func (k *K8s) WaitForRollout(ctx context.Context, t model.Target, timeout time.Duration) error {
	if k.cs == nil {
		return ErrNoCluster
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		st, err := k.rolloutStatus(ctx, t)
		if err != nil {
			return err
		}
		if st.Failed {
			return fmt.Errorf("rollout failed: %s", st.Message)
		}
		if st.Done {
			return nil
		}
		if time.Now().After(deadline) {
			// Include any pod-level reason to explain the stall.
			reason := k.podFailureReason(ctx, t)
			if reason != "" {
				return fmt.Errorf("rollout timed out: %s", reason)
			}
			return fmt.Errorf("rollout timed out after %s: %s", timeout, st.Message)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (k *K8s) rolloutStatus(ctx context.Context, t model.Target) (RolloutStatus, error) {
	switch t.Kind {
	case model.KindStatefulSet:
		ss, err := k.cs.AppsV1().StatefulSets(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
		if err != nil {
			return RolloutStatus{}, err
		}
		s := ss.Status
		replicas := int32(1)
		if ss.Spec.Replicas != nil {
			replicas = *ss.Spec.Replicas
		}
		if s.ObservedGeneration >= ss.Generation &&
			s.UpdatedReplicas == replicas && s.ReadyReplicas == replicas &&
			(s.UpdateRevision == "" || s.UpdateRevision == s.CurrentRevision) {
			return RolloutStatus{Done: true}, nil
		}
		return RolloutStatus{Message: fmt.Sprintf("%d/%d ready, %d updated", s.ReadyReplicas, replicas, s.UpdatedReplicas)}, nil
	default:
		d, err := k.cs.AppsV1().Deployments(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
		if err != nil {
			return RolloutStatus{}, err
		}
		s := d.Status
		for _, c := range s.Conditions {
			if c.Type == "Progressing" && c.Reason == "ProgressDeadlineExceeded" {
				return RolloutStatus{Failed: true, Message: c.Message}, nil
			}
		}
		replicas := int32(1)
		if d.Spec.Replicas != nil {
			replicas = *d.Spec.Replicas
		}
		if s.ObservedGeneration >= d.Generation &&
			s.UpdatedReplicas == replicas && s.ReadyReplicas == replicas &&
			s.AvailableReplicas == replicas && s.UnavailableReplicas == 0 {
			return RolloutStatus{Done: true}, nil
		}
		return RolloutStatus{Message: fmt.Sprintf("%d/%d ready, %d updated, %d unavailable",
			s.ReadyReplicas, replicas, s.UpdatedReplicas, s.UnavailableReplicas)}, nil
	}
}

// podFailureReason inspects pods for the workload and returns the first waiting
// reason it finds (e.g. ImagePullBackOff, CrashLoopBackOff).
func (k *K8s) podFailureReason(ctx context.Context, t model.Target) string {
	selector, err := k.podSelector(ctx, t)
	if err != nil || selector == "" {
		return ""
	}
	pods, err := k.cs.CoreV1().Pods(t.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return ""
	}
	for _, p := range pods.Items {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
				return fmt.Sprintf("%s: %s", cs.State.Waiting.Reason, cs.State.Waiting.Message)
			}
		}
	}
	return ""
}

func (k *K8s) podSelector(ctx context.Context, t model.Target) (string, error) {
	var labels map[string]string
	switch t.Kind {
	case model.KindStatefulSet:
		ss, err := k.cs.AppsV1().StatefulSets(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		if ss.Spec.Selector != nil {
			labels = ss.Spec.Selector.MatchLabels
		}
	default:
		d, err := k.cs.AppsV1().Deployments(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		if d.Spec.Selector != nil {
			labels = d.Spec.Selector.MatchLabels
		}
	}
	return metav1.FormatLabelSelector(&metav1.LabelSelector{MatchLabels: labels}), nil
}

// TargetStatus is a snapshot of a target's live state for the UI.
type TargetStatus struct {
	Target        model.Target `json:"target"`
	CurrentImage  string       `json:"current_image"`
	ReadyReplicas int32        `json:"ready_replicas"`
	Replicas      int32        `json:"replicas"`
	Available     bool         `json:"available"`
	Error         string       `json:"error,omitempty"`
}

// Status returns a live snapshot of a target workload.
func (k *K8s) Status(ctx context.Context, t model.Target) TargetStatus {
	ts := TargetStatus{Target: t}
	if k.cs == nil {
		ts.Error = ErrNoCluster.Error()
		return ts
	}
	switch t.Kind {
	case model.KindStatefulSet:
		ss, err := k.cs.AppsV1().StatefulSets(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
		if err != nil {
			ts.Error = err.Error()
			return ts
		}
		if ss.Spec.Replicas != nil {
			ts.Replicas = *ss.Spec.Replicas
		}
		ts.ReadyReplicas = ss.Status.ReadyReplicas
		ts.CurrentImage = imageOf(ss.Spec.Template.Spec.Containers, t.Container)
	default:
		d, err := k.cs.AppsV1().Deployments(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
		if err != nil {
			ts.Error = err.Error()
			return ts
		}
		if d.Spec.Replicas != nil {
			ts.Replicas = *d.Spec.Replicas
		}
		ts.ReadyReplicas = d.Status.ReadyReplicas
		ts.CurrentImage = imageOf(d.Spec.Template.Spec.Containers, t.Container)
	}
	ts.Available = ts.Replicas > 0 && ts.ReadyReplicas == ts.Replicas
	return ts
}

func imageOf(containers []corev1.Container, name string) string {
	for _, c := range containers {
		if c.Name == name {
			return c.Image
		}
	}
	return ""
}
