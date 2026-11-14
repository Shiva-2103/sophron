package rules

// Detector looks for one kind of problem.
type Detector interface {
	Name() string
	Description() string
	Detect(in Input) []Finding
}

type detector struct {
	name, description string
	detect            func(in Input) []Finding
}

func (d detector) Name() string              { return d.name }
func (d detector) Description() string       { return d.description }
func (d detector) Detect(in Input) []Finding { return d.detect(in) }

// Detector names. They appear in findings, logs and the API.
const (
	PendingUnschedulable = "pending-unschedulable"
	RolloutStuck         = "rollout-stuck"
	OOMKilled            = "oom-killed"
	ContainerFailing     = "container-failing"
	SlowColdStart        = "slow-cold-start"
	QueueBacklog         = "queue-backlog"
)

// All returns every built-in detector.
func All() []Detector {
	return []Detector{
		detector{PendingUnschedulable, "A model pod can't be placed on any node (no CPU, memory or GPU room, or no matching node).", detectPending},
		detector{RolloutStuck, "A new version isn't becoming Ready while the old version keeps serving.", detectRolloutStuck},
		detector{OOMKilled, "A model server was killed for using more memory than its limit.", detectOOM},
		detector{ContainerFailing, "A model server keeps crashing, or its container can't be pulled or created.", detectContainerFailing},
		detector{SlowColdStart, "A model pod takes too long to become Ready (model download or load).", detectSlowStart},
		detector{QueueBacklog, "Requests keep waiting because every serving slot is busy.", detectQueueBacklog},
	}
}
