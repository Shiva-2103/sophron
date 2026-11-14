package rules

import (
	"fmt"
	"strings"
	"time"

	"github.com/Shiva-2103/sophron/internal/collector"
)

// unscheduled reports whether a pod is waiting for a node.
func unscheduled(s collector.Snapshot) bool {
	return s.Phase == "Pending" && s.Node == ""
}

// schedulingProblem turns the scheduler's FailedScheduling message into a
// short cause and a fix. Example message:
//
//	0/3 nodes are available: 1 Insufficient nvidia.com/gpu,
//	2 node(s) didn't match Pod's node affinity/selector.
//
// "Insufficient ..." is the real blocker; the selector part only explains
// why the other nodes were never candidates.
func schedulingProblem(s collector.Snapshot) (cause string, fix Fix) {
	msg := ""
	if s.WarningReason == "FailedScheduling" {
		msg = s.WarningMessage
	}
	target := kubectlTarget(s)
	switch {
	case strings.Contains(msg, "Insufficient nvidia.com/gpu"):
		return "it needs a GPU and no node has a free one", Fix{
			Summary: "Give it a GPU node, or don't ask for a GPU.",
			Steps: []string{
				"Check a GPU node pool exists and the autoscaler is allowed to add a node to it.",
				"Check GPU nodes advertise GPUs: the NVIDIA device plugin (or GPU Operator) must be running.",
				"If this model fits on CPU, remove the nvidia.com/gpu request.",
			},
			Snippet: `kubectl get nodes -o custom-columns='NODE:.metadata.name,GPUS:.status.allocatable.nvidia\.com/gpu'`,
		}
	case strings.Contains(msg, "Insufficient cpu"):
		return fmt.Sprintf("it asks for %s CPU cores and no matching node has that much free", cores(s.CPURequestCores)), Fix{
			Summary: "Ask for less CPU, or add capacity.",
			Steps: []string{
				fmt.Sprintf("Lower resources.requests.cpu (now %s) if the model doesn't need it all.", cores(s.CPURequestCores)),
				"Or add a node, or use a bigger instance type for this node pool.",
				"Or check what else is using the node: kubectl describe node <node> (Allocated resources).",
			},
			Snippet: fmt.Sprintf("kubectl -n %s set resources %s --requests=cpu=%s", s.Namespace, target, cores(max(s.CPURequestCores/2, 0.5))),
		}
	case strings.Contains(msg, "Insufficient memory"):
		return "it asks for more memory than any matching node has free", Fix{
			Summary: "Ask for less memory, or add capacity.",
			Steps: []string{
				"Lower resources.requests.memory if the model doesn't need it all.",
				"Or add a node, or use an instance type with more memory.",
			},
		}
	case strings.Contains(msg, "didn't match Pod's node affinity/selector"):
		return "no node matches its nodeSelector or affinity", Fix{
			Summary: "Make the node selector match a real node.",
			Steps: []string{
				"Compare the pod's nodeSelector/affinity with your nodes' labels.",
				"Fix the label on the node pool, or the selector on the workload.",
			},
			Snippet: "kubectl get nodes --show-labels",
		}
	case strings.Contains(msg, "untolerated taint"):
		return "every matching node has a taint the pod doesn't tolerate", Fix{
			Summary: "Add the right toleration, or remove the taint.",
			Steps:   []string{"GPU node pools are often tainted (for example nvidia.com/gpu=present:NoSchedule). Add a matching toleration to the workload."},
		}
	case strings.Contains(msg, "PersistentVolumeClaim"), strings.Contains(msg, "persistentvolumeclaim"):
		return "its storage (PersistentVolumeClaim) isn't ready", Fix{
			Summary: "Fix the volume claim.",
			Steps:   []string{"kubectl describe pvc in this namespace; check the StorageClass exists and can provision volumes."},
		}
	}
	return "Kubernetes can't find a node for it", Fix{
		Summary: "Look at the scheduler's reason.",
		Steps:   []string{"The Events at the bottom of kubectl describe pod explain why."},
		Snippet: fmt.Sprintf("kubectl -n %s describe pod %s", s.Namespace, s.Pod),
	}
}

func schedulingEvidence(s collector.Snapshot, waited time.Duration) []string {
	ev := []string{fmt.Sprintf("pod %s has been Pending for %s with no node", s.Pod, humanDur(waited))}
	if s.WarningReason == "FailedScheduling" {
		ev = append(ev, "scheduler: "+s.WarningMessage)
	}
	if s.CPURequestCores > 0 {
		ev = append(ev, fmt.Sprintf("requests %s CPU cores", cores(s.CPURequestCores)))
	}
	if s.GPURequest > 0 {
		ev = append(ev, fmt.Sprintf("requests %d GPU(s)", s.GPURequest))
	}
	return ev
}

// pendingFor is how long a pod has waited for a node.
func pendingFor(in Input, s collector.Snapshot) time.Duration {
	if d := age(in.Now, s.CreatedAt); d > 0 {
		return d
	}
	// Older snapshots have no creation time: use the history instead.
	hist := in.HistoryOf(s)
	n := trailing(hist, unscheduled)
	if n == 0 {
		return 0
	}
	return in.Now.Sub(hist[len(hist)-n].Time)
}

// inRollout reports whether s is a new version waiting while another version
// of the same workload is Ready. The rollout-stuck detector owns that case.
func inRollout(in Input, s collector.Snapshot) bool {
	if s.Revision == "" {
		return false
	}
	for _, o := range in.WorkloadPods(s) {
		if o.Ready && o.Revision != "" && o.Revision != s.Revision {
			return true
		}
	}
	return false
}

func anyReady(pods []collector.Snapshot) bool {
	for _, p := range pods {
		if p.Ready {
			return true
		}
	}
	return false
}

func detectPending(in Input) []Finding {
	var out []Finding
	for _, s := range in.Latest {
		if !unscheduled(s) || inRollout(in, s) {
			continue
		}
		waited := pendingFor(in, s)
		if waited < in.Config.PendingAfter {
			continue
		}
		cause, fix := schedulingProblem(s)
		sev, impact := Warning, "Other replicas are still serving."
		if !anyReady(in.WorkloadPods(s)) {
			sev, impact = Critical, "No replica is Ready, so this model is not serving any requests."
		}
		out = append(out, newFinding(PendingUnschedulable, s, sev,
			fmt.Sprintf("%s can't be scheduled: %s", name(s), cause),
			fmt.Sprintf("Pod %s has waited %s for a node because %s. %s", s.Pod, humanDur(waited), cause, impact),
			schedulingEvidence(s, waited), fix))
	}
	return out
}

func detectRolloutStuck(in Input) []Finding {
	var out []Finding
	seen := map[string]bool{}
	for _, s := range in.Latest {
		if s.Ready || s.Revision == "" || s.WorkloadName == "" || !inRollout(in, s) {
			continue
		}
		waited := age(in.Now, s.CreatedAt)
		if waited < in.Config.RolloutStuckAfter {
			continue
		}
		key := name(s)
		if seen[key] {
			continue // one finding per workload
		}
		seen[key] = true

		var oldPods []string
		for _, o := range in.WorkloadPods(s) {
			if o.Ready && o.Revision != s.Revision {
				oldPods = append(oldPods, o.Pod)
			}
		}
		evidence := []string{
			fmt.Sprintf("new version (revision %s): pod %s not Ready after %s", s.Revision, s.Pod, humanDur(waited)),
			fmt.Sprintf("old version still serving: %s", strings.Join(oldPods, ", ")),
		}
		why := "the new pod isn't Ready yet"
		fix := Fix{
			Summary: "Fix what blocks the new version, or roll back.",
			Steps: []string{
				"Find out why the new pod isn't Ready: kubectl describe pod (Events) and kubectl logs.",
				fmt.Sprintf("To go back to the old version: kubectl -n %s rollout undo %s", s.Namespace, kubectlTarget(s)),
			},
		}
		switch {
		case unscheduled(s):
			cause, _ := schedulingProblem(s)
			why = "the new pod can't be scheduled: " + cause
			if s.WarningReason == "FailedScheduling" {
				evidence = append(evidence, "scheduler: "+s.WarningMessage)
			}
			if strings.Contains(s.WarningMessage, "Insufficient") {
				// Our lab lesson: a rolling update needs room for old AND new
				// at once. Big model pods often don't have that room.
				fix = Fix{
					Summary: "There's no room to run the old and new version side by side. Replace pods instead of adding first.",
					Steps: []string{
						"A rolling update starts the new pod before stopping the old one, so it briefly needs double the resources.",
						"Use strategy Recreate, or RollingUpdate with maxSurge 0, so the old pod stops first. Expect a short gap in serving.",
						"Or add capacity so both fit during the rollout.",
					},
					Snippet: "spec:\n  strategy:\n    type: RollingUpdate\n    rollingUpdate:\n      maxSurge: 0\n      maxUnavailable: 1",
				}
			}
		case s.WaitingReason != "":
			why = "the new pod's container is " + s.WaitingReason
			evidence = append(evidence, "container waiting: "+s.WaitingReason)
		case s.LastTerminationReason != "":
			why = "the new pod's container last stopped with " + s.LastTerminationReason
			evidence = append(evidence, "last termination: "+s.LastTerminationReason)
		}
		out = append(out, newFinding(RolloutStuck, s, Warning,
			fmt.Sprintf("Rollout of %s is stuck: %s", name(s), why),
			fmt.Sprintf("The new version of %s has not become Ready in %s, so your change is not live. The old version is still serving.", name(s), humanDur(waited)),
			evidence, fix))
	}
	return out
}
