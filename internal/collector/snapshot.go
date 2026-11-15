package collector

import "time"

// Snapshot is everything Sophron knows about one model pod at one moment.
// The rules engine (Phase 3) and cost engine (Phase 4) read these.
//
// Metric fields are pointers: nil means "no data", which is different from 0.
// A CPU-only cluster has no GPU metrics; that must not look like an idle GPU.
type Snapshot struct {
	Time      time.Time `json:"time"`
	Namespace string    `json:"namespace"`
	Pod       string    `json:"pod"`
	Server    string    `json:"server"` // llamacpp, vllm, or the label value

	// From Kubernetes: the pod.
	WorkloadKind string `json:"workloadKind,omitempty"`
	WorkloadName string `json:"workloadName,omitempty"`
	// The model server container (the one that last failed, else the first).
	Container string `json:"container,omitempty"`
	// Which version of the workload this pod runs (pod-template-hash or
	// controller-revision-hash). Two revisions at once means a rollout.
	Revision              string  `json:"revision,omitempty"`
	Phase                 string  `json:"phase"`
	Ready                 bool    `json:"ready"`
	Restarts              int32   `json:"restarts"`
	WaitingReason         string  `json:"waitingReason,omitempty"`
	LastTerminationReason string  `json:"lastTerminationReason,omitempty"`
	CPURequestCores       float64 `json:"cpuRequestCores"`
	CPULimitCores         float64 `json:"cpuLimitCores"`
	MemoryLimitBytes      float64 `json:"memoryLimitBytes"`
	MemoryRequestBytes    float64 `json:"memoryRequestBytes"`
	GPURequest            int64   `json:"gpuRequest"`
	// Seconds from pod start to Ready. Feeds the slow-cold-start detector.
	StartupSeconds *float64 `json:"startupSeconds,omitempty"`
	// When the pod was created, started (scheduled, init began) and last became Ready.
	CreatedAt time.Time `json:"createdAt,omitzero"`
	StartedAt time.Time `json:"startedAt,omitzero"`
	ReadyAt   time.Time `json:"readyAt,omitzero"`

	// From Kubernetes: the latest warning event for this pod (last 15 minutes).
	WarningReason  string `json:"warningReason,omitempty"`
	WarningMessage string `json:"warningMessage,omitempty"`

	// From Kubernetes: the node.
	Node             string  `json:"node,omitempty"`
	NodeProvider     string  `json:"nodeProvider,omitempty"`
	NodeInstanceType string  `json:"nodeInstanceType,omitempty"`
	NodeZone         string  `json:"nodeZone,omitempty"`
	NodeRegion       string  `json:"nodeRegion,omitempty"`
	NodeCapacityType string  `json:"nodeCapacityType,omitempty"`
	NodeCPUCores     float64 `json:"nodeCpuCores,omitempty"`
	NodeMemoryBytes  float64 `json:"nodeMemoryBytes,omitempty"`
	NodeGPUs         int64   `json:"nodeGpus,omitempty"`

	// From Prometheus.
	GenerationTokensPerSecond *float64 `json:"generationTokensPerSecond"`
	PromptTokensPerSecond     *float64 `json:"promptTokensPerSecond"`
	RequestsRunning           *float64 `json:"requestsRunning"`
	RequestsWaiting           *float64 `json:"requestsWaiting"`
	KVCacheUsageRatio         *float64 `json:"kvCacheUsageRatio"`
	CPUCores                  *float64 `json:"cpuCores"`
	CPUThrottledRatio         *float64 `json:"cpuThrottledRatio"`
	MemoryWorkingSetBytes     *float64 `json:"memoryWorkingSetBytes"`
	GPUUtilizationRatio       *float64 `json:"gpuUtilizationRatio"`
	GPUMemoryUsedBytes        *float64 `json:"gpuMemoryUsedBytes"`

	// From the cost engine (Phase 4). Nil when the pod has no node yet.
	// CostPerHour is this pod's share of its node, in USD.
	CostPerHour     *float64 `json:"costPerHour"`
	NodeCostPerHour *float64 `json:"nodeCostPerHour"`
	// Where the node price came from: node label, price file, opencost, default.
	PriceSource string `json:"priceSource,omitempty"`
}
