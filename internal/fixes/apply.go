package fixes

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	"github.com/Shiva-2103/sophron/internal/rules"
)

// FieldManager is how Sophron's changes show up in managedFields, so the
// resolved alert can say "changed with sophron".
const FieldManager = "sophron"

// KubeApplier makes changes with client-go. It only acts on Deployments and
// StatefulSets, in the namespaces its RBAC allows.
type KubeApplier struct {
	Client      kubernetes.Interface
	MaxReplicas int32 // scale-up never goes above this (default 5)
}

// Apply makes one change and says what it did.
func (k KubeApplier) Apply(ctx context.Context, a rules.Action) (string, error) {
	switch a.Kind {
	case rules.ActionSetMemoryLimit:
		return k.setMemoryLimit(ctx, a)
	case rules.ActionScaleUp:
		return k.scaleUp(ctx, a)
	case rules.ActionRestart:
		return k.restart(ctx, a)
	case rules.ActionRolloutUndo:
		return k.rolloutUndo(ctx, a)
	}
	return "", fmt.Errorf("unknown action %q", a.Kind)
}

var update = metav1.UpdateOptions{FieldManager: FieldManager}

// template runs fn on the workload's pod template and saves it, retrying
// when someone else changed the object at the same time.
func (k KubeApplier) template(ctx context.Context, a rules.Action, fn func(*corev1.PodTemplateSpec) error) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		switch a.WorkloadKind {
		case "Deployment":
			d, err := k.Client.AppsV1().Deployments(a.Namespace).Get(ctx, a.WorkloadName, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if err := fn(&d.Spec.Template); err != nil {
				return err
			}
			_, err = k.Client.AppsV1().Deployments(a.Namespace).Update(ctx, d, update)
			return err
		case "StatefulSet":
			s, err := k.Client.AppsV1().StatefulSets(a.Namespace).Get(ctx, a.WorkloadName, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if err := fn(&s.Spec.Template); err != nil {
				return err
			}
			_, err = k.Client.AppsV1().StatefulSets(a.Namespace).Update(ctx, s, update)
			return err
		}
		return fmt.Errorf("can't change a %s", a.WorkloadKind)
	})
}

func (k KubeApplier) setMemoryLimit(ctx context.Context, a rules.Action) (string, error) {
	want, err := resource.ParseQuantity(a.Value)
	if err != nil {
		return "", fmt.Errorf("bad memory value %q: %w", a.Value, err)
	}
	var before string
	err = k.template(ctx, a, func(t *corev1.PodTemplateSpec) error {
		for i := range t.Spec.Containers {
			c := &t.Spec.Containers[i]
			if c.Name != a.Container {
				continue
			}
			if c.Resources.Limits == nil {
				c.Resources.Limits = corev1.ResourceList{}
			}
			old := c.Resources.Limits[corev1.ResourceMemory]
			before = old.String()
			if old.Cmp(want) >= 0 {
				return fmt.Errorf("the limit is already %s, at or above %s", before, a.Value)
			}
			c.Resources.Limits[corev1.ResourceMemory] = want
			// A request above the limit is invalid.
			if req, ok := c.Resources.Requests[corev1.ResourceMemory]; ok && req.Cmp(want) > 0 {
				c.Resources.Requests[corev1.ResourceMemory] = want
			}
			return nil
		}
		return fmt.Errorf("container %q not found", a.Container)
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("memory limit of %s changed from %s to %s; Kubernetes is rolling out new pods", a.Container, before, a.Value), nil
}

func (k KubeApplier) scaleUp(ctx context.Context, a rules.Action) (string, error) {
	maxR := k.MaxReplicas
	if maxR == 0 {
		maxR = 5
	}
	var from, to int32
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var err error
		switch a.WorkloadKind {
		case "Deployment":
			s, e := k.Client.AppsV1().Deployments(a.Namespace).GetScale(ctx, a.WorkloadName, metav1.GetOptions{})
			if e != nil {
				return e
			}
			from, to = s.Spec.Replicas, s.Spec.Replicas+1
			if to > maxR {
				return fmt.Errorf("already at %d replicas; the policy allows at most %d", from, maxR)
			}
			s.Spec.Replicas = to
			_, err = k.Client.AppsV1().Deployments(a.Namespace).UpdateScale(ctx, a.WorkloadName, s, update)
		case "StatefulSet":
			s, e := k.Client.AppsV1().StatefulSets(a.Namespace).GetScale(ctx, a.WorkloadName, metav1.GetOptions{})
			if e != nil {
				return e
			}
			from, to = s.Spec.Replicas, s.Spec.Replicas+1
			if to > maxR {
				return fmt.Errorf("already at %d replicas; the policy allows at most %d", from, maxR)
			}
			s.Spec.Replicas = to
			_, err = k.Client.AppsV1().StatefulSets(a.Namespace).UpdateScale(ctx, a.WorkloadName, s, update)
		default:
			return fmt.Errorf("can't scale a %s", a.WorkloadKind)
		}
		return err
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("replicas changed from %d to %d", from, to), nil
}

func (k KubeApplier) restart(ctx context.Context, a rules.Action) (string, error) {
	err := k.template(ctx, a, func(t *corev1.PodTemplateSpec) error {
		if t.Annotations == nil {
			t.Annotations = map[string]string{}
		}
		t.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().UTC().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return "", err
	}
	return "pods restarted one by one (rollout restart)", nil
}

// rolloutUndo does what `kubectl rollout undo` does for a Deployment: copy
// the previous ReplicaSet's pod template back into the Deployment.
func (k KubeApplier) rolloutUndo(ctx context.Context, a rules.Action) (string, error) {
	if a.WorkloadKind != "Deployment" {
		return "", fmt.Errorf("rollback is only supported for Deployments")
	}
	var prevRev int64
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		d, err := k.Client.AppsV1().Deployments(a.Namespace).Get(ctx, a.WorkloadName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		prev, err := k.previousReplicaSet(ctx, d)
		if err != nil {
			return err
		}
		prevRev, _ = strconv.ParseInt(prev.Annotations["deployment.kubernetes.io/revision"], 10, 64)
		t := prev.Spec.Template.DeepCopy()
		delete(t.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
		d.Spec.Template = *t
		_, err = k.Client.AppsV1().Deployments(a.Namespace).Update(ctx, d, update)
		return err
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("rolled back to revision %d", prevRev), nil
}

func (k KubeApplier) previousReplicaSet(ctx context.Context, d *appsv1.Deployment) (*appsv1.ReplicaSet, error) {
	sel, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
	if err != nil {
		return nil, err
	}
	list, err := k.Client.AppsV1().ReplicaSets(d.Namespace).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return nil, err
	}
	cur, _ := strconv.ParseInt(d.Annotations["deployment.kubernetes.io/revision"], 10, 64)
	var owned []appsv1.ReplicaSet
	for _, rs := range list.Items {
		if metav1.IsControlledBy(&rs, d) {
			owned = append(owned, rs)
		}
	}
	rev := func(rs appsv1.ReplicaSet) int64 {
		v, _ := strconv.ParseInt(rs.Annotations["deployment.kubernetes.io/revision"], 10, 64)
		return v
	}
	sort.Slice(owned, func(i, j int) bool { return rev(owned[i]) > rev(owned[j]) })
	for i := range owned {
		if r := rev(owned[i]); r > 0 && r < cur {
			return &owned[i], nil
		}
	}
	return nil, fmt.Errorf("no earlier revision of %s to roll back to", d.Name)
}
