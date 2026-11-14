// Package rules is Sophron's SRE brain: detectors read recent snapshots and
// report known LLM-serving problems as findings, each with evidence and a
// suggested fix. Detectors are plain Go functions with no AI and no side
// effects, so every one of them can be unit-tested.
package rules

import (
	"time"
)

// Severity says how urgent a finding is.
type Severity string

// Severities, from least to most urgent.
const (
	Info     Severity = "info"
	Warning  Severity = "warning"
	Critical Severity = "critical"
)

// Rank orders severities, so the engine can keep the most urgent one.
func (s Severity) Rank() int {
	switch s {
	case Critical:
		return 3
	case Warning:
		return 2
	case Info:
		return 1
	}
	return 0
}

// Status of a finding.
const (
	StatusOpen     = "open"
	StatusResolved = "resolved"
)

// Fix is a suggested remedy. Sophron only suggests in Phase 3; applying fixes
// (with a human's approval) comes in Phase 6.
type Fix struct {
	Summary string   `json:"summary"`
	Steps   []string `json:"steps"`
	// Optional YAML or command to copy, already filled in with real values.
	Snippet string `json:"snippet,omitempty"`
}

// Finding is one problem Sophron noticed.
type Finding struct {
	ID       int64    `json:"id,omitempty"`
	Key      string   `json:"key"` // stable identity: same problem, same key
	Detector string   `json:"detector"`
	Severity Severity `json:"severity"`

	Namespace    string `json:"namespace"`
	Pod          string `json:"pod,omitempty"`
	WorkloadKind string `json:"workloadKind,omitempty"`
	WorkloadName string `json:"workloadName,omitempty"`

	Title    string   `json:"title"`    // one line, for a list or a Slack header
	Summary  string   `json:"summary"`  // what is wrong, in plain English
	Evidence []string `json:"evidence"` // the facts that triggered it
	Fix      Fix      `json:"fix"`

	Status     string    `json:"status"`
	FirstSeen  time.Time `json:"firstSeen"`
	LastSeen   time.Time `json:"lastSeen"`
	ResolvedAt time.Time `json:"resolvedAt,omitzero"`
}

// Target is the object a finding is about: the workload when known
// (pods come and go, the workload stays), otherwise the pod.
func (f Finding) Target() string {
	if f.WorkloadName != "" {
		return f.Namespace + "/" + f.WorkloadName
	}
	return f.Namespace + "/" + f.Pod
}

// MakeKey builds the stable key: detector plus target. A crashing pod that is
// replaced by a new pod with a new name keeps the same finding.
func (f Finding) MakeKey() string {
	return f.Detector + "/" + f.Target()
}
