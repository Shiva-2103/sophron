package kube

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Changes finds which tool last changed a workload, from its managedFields.
//
// Every write to a Kubernetes object is recorded per "field manager": the
// tool that sent it (kubectl-client-side-apply, kubectl-patch, helm,
// argocd-controller, ...) and when. Kubernetes records the tool, not the
// person; the person is only in the API server's audit log.
type Changes struct{ Client kubernetes.Interface }

// LastChange returns the field manager that most recently changed the
// workload's spec, and when.
func (c Changes) LastChange(ctx context.Context, kind, namespace, name string) (string, time.Time, error) {
	var (
		fields []metav1.ManagedFieldsEntry
		err    error
	)
	opts := metav1.GetOptions{}
	switch kind {
	case "Deployment":
		o, e := c.Client.AppsV1().Deployments(namespace).Get(ctx, name, opts)
		if e == nil {
			fields = o.ManagedFields
		}
		err = e
	case "StatefulSet":
		o, e := c.Client.AppsV1().StatefulSets(namespace).Get(ctx, name, opts)
		if e == nil {
			fields = o.ManagedFields
		}
		err = e
	case "DaemonSet":
		o, e := c.Client.AppsV1().DaemonSets(namespace).Get(ctx, name, opts)
		if e == nil {
			fields = o.ManagedFields
		}
		err = e
	default:
		return "", time.Time{}, fmt.Errorf("unsupported workload kind %q", kind)
	}
	if err != nil {
		return "", time.Time{}, err
	}
	manager, at := lastSpecChange(fields)
	return manager, at, nil
}

// lastSpecChange picks the newest entry that changed the object itself, not
// its status, and wasn't a Kubernetes controller.
func lastSpecChange(fields []metav1.ManagedFieldsEntry) (string, time.Time) {
	var (
		manager string
		at      time.Time
	)
	for _, f := range fields {
		if f.Subresource != "" || f.Time == nil || f.Manager == "kube-controller-manager" {
			continue
		}
		if t := f.Time.UTC(); t.After(at) {
			manager, at = f.Manager, t
		}
	}
	return manager, at
}
