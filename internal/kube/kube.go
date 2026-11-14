// Package kube keeps a live, in-memory view of pods, nodes and warning events
// using client-go informers, and turns them into the small facts Sophron needs.
//
// Informers do one LIST, then keep the copy up to date with a WATCH stream.
// Reading from them costs no API calls, so the collector can look at every
// pod every 30 seconds without loading the API server.
package kube

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

// ModelServerLabel marks a pod as a model server, even before it exposes
// metrics (for example while Pending). Its value names the server type.
const ModelServerLabel = "sophron.dev/model-server"

// PodKey identifies a pod.
type PodKey struct {
	Namespace string
	Name      string
}

func (k PodKey) String() string { return k.Namespace + "/" + k.Name }

// PodInfo holds the pod facts a snapshot needs.
type PodInfo struct {
	Key                   PodKey
	Node                  string
	ServerLabel           string // value of sophron.dev/model-server, if set
	WorkloadKind          string // Deployment, StatefulSet, ...
	WorkloadName          string
	Revision              string // pod-template-hash or controller-revision-hash
	Phase                 string
	Ready                 bool
	Restarts              int32
	WaitingReason         string // e.g. CrashLoopBackOff, ImagePullBackOff
	LastTerminationReason string // e.g. OOMKilled, Error
	CPURequestCores       float64
	CPULimitCores         float64
	MemoryLimitBytes      float64
	GPURequest            int64
	CreatedAt             time.Time
	StartedAt             time.Time
	ReadyAt               time.Time
}

// NodeInfo holds the node facts a snapshot needs.
type NodeInfo struct {
	Name         string
	Provider     string // aws, gce, azure, kind, ...
	InstanceType string
	Zone         string
	CapacityType string // on-demand, spot, or empty
	CPUCores     float64
	MemoryBytes  float64
	GPUs         int64
}

// Event is the latest warning event for a pod.
type Event struct {
	Reason  string
	Message string
	Time    time.Time
}

// Cache is the read side of the informers.
type Cache interface {
	ModelPods() ([]PodInfo, error)
	Pod(key PodKey) (PodInfo, bool)
	Node(name string) (NodeInfo, bool)
	LastWarning(key PodKey) (Event, bool)
}

// Informers implements Cache with client-go shared informers.
type Informers struct {
	pods      corelisters.PodLister
	nodes     corelisters.NodeLister
	synced    []cache.InformerSynced
	mu        sync.RWMutex
	warnings  map[PodKey]Event
	factories []informers.SharedInformerFactory
}

// NewInformers sets up pod, node and warning-event informers.
// Call Start, then WaitForSync, before reading.
func NewInformers(client kubernetes.Interface, resync time.Duration) *Informers {
	main := informers.NewSharedInformerFactory(client, resync)
	// A second factory with a field selector: only Warning events, so we
	// don't hold every Normal event of the cluster in memory.
	events := informers.NewSharedInformerFactoryWithOptions(client, resync,
		informers.WithTweakListOptions(func(o *metav1.ListOptions) {
			o.FieldSelector = fields.OneTermEqualSelector("type", corev1.EventTypeWarning).String()
		}))

	inf := &Informers{
		pods:      main.Core().V1().Pods().Lister(),
		nodes:     main.Core().V1().Nodes().Lister(),
		warnings:  map[PodKey]Event{},
		factories: []informers.SharedInformerFactory{main, events},
	}
	evInf := events.Core().V1().Events().Informer()
	_, _ = evInf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    inf.onEvent,
		UpdateFunc: func(_, obj any) { inf.onEvent(obj) },
	})
	inf.synced = []cache.InformerSynced{
		main.Core().V1().Pods().Informer().HasSynced,
		main.Core().V1().Nodes().Informer().HasSynced,
		evInf.HasSynced,
	}
	return inf
}

// Start runs the informers until ctx is cancelled.
func (i *Informers) Start(ctx context.Context) {
	for _, f := range i.factories {
		f.Start(ctx.Done())
	}
}

// WaitForSync blocks until the first LIST of every informer finished.
func (i *Informers) WaitForSync(ctx context.Context) bool {
	return cache.WaitForCacheSync(ctx.Done(), i.synced...)
}

// HasSynced reports whether all informers finished their first LIST.
func (i *Informers) HasSynced() bool {
	for _, s := range i.synced {
		if !s() {
			return false
		}
	}
	return true
}

func (i *Informers) onEvent(obj any) {
	ev, ok := obj.(*corev1.Event)
	if !ok || ev.InvolvedObject.Kind != "Pod" {
		return
	}
	t := eventTime(ev)
	key := PodKey{Namespace: ev.InvolvedObject.Namespace, Name: ev.InvolvedObject.Name}
	i.mu.Lock()
	defer i.mu.Unlock()
	if cur, ok := i.warnings[key]; ok && cur.Time.After(t) {
		return
	}
	i.warnings[key] = Event{Reason: ev.Reason, Message: ev.Message, Time: t}
	// Keep the map small: forget warnings older than an hour.
	for k, e := range i.warnings {
		if time.Since(e.Time) > time.Hour {
			delete(i.warnings, k)
		}
	}
}

func eventTime(ev *corev1.Event) time.Time {
	switch {
	case !ev.LastTimestamp.IsZero():
		return ev.LastTimestamp.Time
	case !ev.EventTime.IsZero():
		return ev.EventTime.Time
	default:
		return ev.CreationTimestamp.Time
	}
}

// ModelPods returns pods labelled as model servers.
func (i *Informers) ModelPods() ([]PodInfo, error) {
	sel, err := labels.Parse(ModelServerLabel)
	if err != nil {
		return nil, err
	}
	pods, err := i.pods.List(sel)
	if err != nil {
		return nil, err
	}
	out := make([]PodInfo, 0, len(pods))
	for _, p := range pods {
		out = append(out, PodInfoFrom(p))
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Key.String() < out[b].Key.String() })
	return out, nil
}

// Pod returns one pod from the cache.
func (i *Informers) Pod(key PodKey) (PodInfo, bool) {
	p, err := i.pods.Pods(key.Namespace).Get(key.Name)
	if err != nil {
		return PodInfo{}, false
	}
	return PodInfoFrom(p), true
}

// Node returns one node from the cache.
func (i *Informers) Node(name string) (NodeInfo, bool) {
	if name == "" {
		return NodeInfo{}, false
	}
	n, err := i.nodes.Get(name)
	if err != nil {
		return NodeInfo{}, false
	}
	return NodeInfoFrom(n), true
}

// LastWarning returns the newest warning event for a pod, if any.
func (i *Informers) LastWarning(key PodKey) (Event, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	e, ok := i.warnings[key]
	return e, ok
}

// PodInfoFrom extracts the facts Sophron needs from a pod.
func PodInfoFrom(p *corev1.Pod) PodInfo {
	info := PodInfo{
		Key:         PodKey{Namespace: p.Namespace, Name: p.Name},
		Node:        p.Spec.NodeName,
		ServerLabel: p.Labels[ModelServerLabel],
		Phase:       string(p.Status.Phase),
	}
	info.WorkloadKind, info.WorkloadName = workload(p)
	info.Revision = first(p.Labels, "pod-template-hash", "controller-revision-hash")
	info.CreatedAt = p.CreationTimestamp.Time
	if p.Status.StartTime != nil {
		info.StartedAt = p.Status.StartTime.Time
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			info.Ready = c.Status == corev1.ConditionTrue
			if info.Ready {
				info.ReadyAt = c.LastTransitionTime.Time
			}
		}
	}
	for _, cs := range p.Status.ContainerStatuses {
		info.Restarts += cs.RestartCount
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" && info.WaitingReason == "" {
			info.WaitingReason = cs.State.Waiting.Reason
		}
		if t := cs.LastTerminationState.Terminated; t != nil && t.Reason != "" && info.LastTerminationReason == "" {
			info.LastTerminationReason = t.Reason
		}
	}
	for _, c := range p.Spec.Containers {
		if q, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
			info.CPURequestCores += q.AsApproximateFloat64()
		}
		if q, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
			info.CPULimitCores += q.AsApproximateFloat64()
		}
		if q, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
			info.MemoryLimitBytes += q.AsApproximateFloat64()
		}
		if q, ok := c.Resources.Limits["nvidia.com/gpu"]; ok {
			info.GPURequest += q.Value()
		}
	}
	return info
}

// workload finds the controller that owns a pod. For a Deployment the owner is
// a ReplicaSet named "<deployment>-<pod-template-hash>", so we strip the hash.
func workload(p *corev1.Pod) (kind, name string) {
	for _, o := range p.OwnerReferences {
		if o.Controller == nil || !*o.Controller {
			continue
		}
		if o.Kind == "ReplicaSet" {
			if h := p.Labels["pod-template-hash"]; h != "" && strings.HasSuffix(o.Name, "-"+h) {
				return "Deployment", strings.TrimSuffix(o.Name, "-"+h)
			}
		}
		return o.Kind, o.Name
	}
	return "", ""
}

// NodeInfoFrom extracts the facts Sophron needs from a node.
func NodeInfoFrom(n *corev1.Node) NodeInfo {
	l := n.Labels
	info := NodeInfo{
		Name:         n.Name,
		Provider:     provider(n.Spec.ProviderID),
		InstanceType: first(l, "node.kubernetes.io/instance-type", "beta.kubernetes.io/instance-type"),
		Zone:         first(l, "topology.kubernetes.io/zone", "failure-domain.beta.kubernetes.io/zone"),
		CapacityType: capacityType(l),
	}
	if q, ok := n.Status.Capacity[corev1.ResourceCPU]; ok {
		info.CPUCores = q.AsApproximateFloat64()
	}
	if q, ok := n.Status.Capacity[corev1.ResourceMemory]; ok {
		info.MemoryBytes = q.AsApproximateFloat64()
	}
	if q, ok := n.Status.Capacity["nvidia.com/gpu"]; ok {
		info.GPUs = q.Value()
	}
	return info
}

func provider(id string) string {
	if p, _, ok := strings.Cut(id, "://"); ok {
		return p
	}
	return ""
}

func first(l map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := l[k]; v != "" {
			return v
		}
	}
	return ""
}

// capacityType normalises the spot/on-demand labels of the main clouds and Karpenter.
func capacityType(l map[string]string) string {
	if v := first(l, "karpenter.sh/capacity-type", "eks.amazonaws.com/capacityType"); v != "" {
		v = strings.ToLower(v)
		if v == "on_demand" {
			return "on-demand"
		}
		return v
	}
	if l["cloud.google.com/gke-spot"] == "true" || l["cloud.google.com/gke-preemptible"] == "true" {
		return "spot"
	}
	if v := l["kubernetes.azure.com/scalesetpriority"]; v != "" {
		if v == "spot" {
			return "spot"
		}
		return "on-demand"
	}
	return ""
}

// Describe returns a short human description, handy in logs.
func (n NodeInfo) Describe() string {
	return fmt.Sprintf("%s (%s %s %s)", n.Name, n.Provider, n.InstanceType, n.CapacityType)
}
