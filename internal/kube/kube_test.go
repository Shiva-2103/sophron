package kube

import (
	"context"
	"reflect"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func ptr[T any](v T) *T { return &v }

func modelPod() *corev1.Pod {
	start := metav1.NewTime(time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC))
	ready := metav1.NewTime(start.Add(90 * time.Second))
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "llm-server-7b895f855b-jx5h6", Namespace: "llm",
			Labels: map[string]string{ModelServerLabel: "llamacpp", "pod-template-hash": "7b895f855b"},
			OwnerReferences: []metav1.OwnerReference{{
				Kind: "ReplicaSet", Name: "llm-server-7b895f855b", Controller: ptr(true),
			}},
		},
		Spec: corev1.PodSpec{
			NodeName: "worker-1",
			Containers: []corev1.Container{{
				Name: "llama-server",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("6"),
						corev1.ResourceMemory: resource.MustParse("6Gi"),
						"nvidia.com/gpu":      resource.MustParse("1"),
					},
				},
			}},
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			StartTime:  &start,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: ready}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "llama-server", RestartCount: 2,
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}},
			}},
		},
	}
}

func TestPodInfoFrom(t *testing.T) {
	info := PodInfoFrom(modelPod())
	if info.WorkloadKind != "Deployment" || info.WorkloadName != "llm-server" {
		t.Errorf("workload = %s/%s, want Deployment/llm-server", info.WorkloadKind, info.WorkloadName)
	}
	if !info.Ready || info.Restarts != 2 || info.LastTerminationReason != "OOMKilled" {
		t.Errorf("status facts wrong: %+v", info)
	}
	if info.CPURequestCores != 4 || info.CPULimitCores != 6 || info.GPURequest != 1 {
		t.Errorf("resources wrong: %+v", info)
	}
	if info.MemoryLimitBytes != 6*1024*1024*1024 {
		t.Errorf("memory limit = %v", info.MemoryLimitBytes)
	}
	if got := info.ReadyAt.Sub(info.StartedAt); got != 90*time.Second {
		t.Errorf("startup = %v, want 90s", got)
	}
}

func TestPodInfoPendingWaiting(t *testing.T) {
	p := modelPod()
	p.Status = corev1.PodStatus{
		Phase: corev1.PodPending,
		ContainerStatuses: []corev1.ContainerStatus{{
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
		}},
	}
	info := PodInfoFrom(p)
	if info.Phase != "Pending" || info.Ready || info.WaitingReason != "ImagePullBackOff" {
		t.Errorf("unexpected: %+v", info)
	}
}

func TestNodeInfoFrom(t *testing.T) {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "ip-10-0-1-5", Labels: map[string]string{
			"node.kubernetes.io/instance-type": "g6.xlarge",
			"topology.kubernetes.io/zone":      "us-east-1b",
			"karpenter.sh/capacity-type":       "spot",
			"topology.kubernetes.io/region":    "us-east-1",
		}, Annotations: map[string]string{HourlyPriceKey: "0.42"}},
		Spec: corev1.NodeSpec{ProviderID: "aws:///us-east-1b/i-0abc"},
		Status: corev1.NodeStatus{Capacity: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("4"), "nvidia.com/gpu": resource.MustParse("1"),
		}},
	}
	info := NodeInfoFrom(n)
	want := NodeInfo{Name: "ip-10-0-1-5", Provider: "aws", InstanceType: "g6.xlarge", Zone: "us-east-1b", Region: "us-east-1",
		CapacityType: "spot", CPUCores: 4, GPUs: 1, HourlyPrice: "0.42"}
	if info.Labels["topology.kubernetes.io/zone"] != "us-east-1b" {
		t.Errorf("labels not kept: %v", info.Labels)
	}
	info.Labels = nil
	if !reflect.DeepEqual(info, want) {
		t.Errorf("got %+v\nwant %+v", info, want)
	}
}

func TestCapacityType(t *testing.T) {
	cases := []struct {
		labels map[string]string
		want   string
	}{
		{map[string]string{"eks.amazonaws.com/capacityType": "ON_DEMAND"}, "on-demand"},
		{map[string]string{"cloud.google.com/gke-spot": "true"}, "spot"},
		{map[string]string{"kubernetes.azure.com/scalesetpriority": "spot"}, "spot"},
		{map[string]string{}, ""},
	}
	for _, c := range cases {
		if got := capacityType(c.labels); got != c.want {
			t.Errorf("%v: got %q, want %q", c.labels, got, c.want)
		}
	}
}

func TestInformersEndToEnd(t *testing.T) {
	pod := modelPod()
	other := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: corev1.PodSpec{NodeName: "worker-1", Containers: []corev1.Container{{Name: "web", Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi")}}}}}}
	done := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: "default"},
		Spec: corev1.PodSpec{NodeName: "worker-1", Containers: []corev1.Container{{Name: "job", Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}, Spec: corev1.NodeSpec{ProviderID: "kind://docker/x"}}
	warning := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "ev1", Namespace: "llm"},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "llm", Name: pod.Name},
		Type:           corev1.EventTypeWarning, Reason: "FailedScheduling", Message: "0/3 nodes are available: Insufficient cpu",
		LastTimestamp: metav1.Now(),
	}
	client := fake.NewClientset(pod, other, done, node, warning)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inf := NewInformers(client, 0)
	inf.Start(ctx)
	if !inf.WaitForSync(ctx) {
		t.Fatal("informers did not sync")
	}

	pods, err := inf.ModelPods()
	if err != nil || len(pods) != 1 || pods[0].Key.Name != pod.Name {
		t.Fatalf("ModelPods = %+v, %v; want only the labelled pod", pods, err)
	}
	if n, ok := inf.Node("worker-1"); !ok || n.Provider != "kind" {
		t.Errorf("Node = %+v, %v", n, ok)
	}
	if ns := inf.Nodes(); len(ns) != 1 || ns[0].Name != "worker-1" {
		t.Errorf("Nodes = %+v", ns)
	}
	// The model pod (4 cores, 1 GPU) and the web pod (0.5 cores, 1 GiB);
	// the finished job no longer holds its 2 cores.
	if r := inf.NodeRequests("worker-1"); r.Pods != 2 || r.CPUCores != 4.5 || r.MemoryBytes != 1<<30 || r.GPUs != 1 {
		t.Errorf("NodeRequests = %+v", r)
	}
	// The event handler runs asynchronously after sync; give it a moment.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if ev, ok := inf.LastWarning(PodKey{"llm", pod.Name}); ok {
			if ev.Reason != "FailedScheduling" {
				t.Errorf("warning reason = %q", ev.Reason)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("warning event not recorded")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLastChange(t *testing.T) {
	t1 := metav1.NewTime(time.Date(2026, 11, 15, 7, 0, 0, 0, time.UTC))
	t2 := metav1.NewTime(t1.Add(10 * time.Minute))
	t3 := metav1.NewTime(t1.Add(20 * time.Minute))
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "llm-server", Namespace: "llm",
		ManagedFields: []metav1.ManagedFieldsEntry{
			{Manager: "kubectl-client-side-apply", Operation: metav1.ManagedFieldsOperationUpdate, Time: &t1},
			{Manager: "kubectl-patch", Operation: metav1.ManagedFieldsOperationUpdate, Time: &t2},
			{Manager: "kube-controller-manager", Operation: metav1.ManagedFieldsOperationUpdate, Time: &t3, Subresource: "status"},
			{Manager: "kube-controller-manager", Operation: metav1.ManagedFieldsOperationUpdate, Time: &t3},
		}}}
	c := Changes{Client: fake.NewClientset(dep)}
	m, at, err := c.LastChange(context.Background(), "Deployment", "llm", "llm-server")
	if err != nil || m != "kubectl-patch" || !at.Equal(t2.Time) {
		t.Errorf("got %q %v %v, want kubectl-patch at %v", m, at, err, t2.Time)
	}
	if _, _, err := c.LastChange(context.Background(), "Job", "llm", "x"); err == nil {
		t.Error("want an error for an unsupported kind")
	}
}
