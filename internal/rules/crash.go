package rules

import (
	"fmt"
	"slices"

	"github.com/Shiva-2103/sophron/internal/collector"
)

var (
	pullReasons   = []string{"ImagePullBackOff", "ErrImagePull", "InvalidImageName"}
	createReasons = []string{"CreateContainerConfigError", "CreateContainerError", "RunContainerError"}
)

func memoryWorkingSet(s collector.Snapshot) *float64 { return s.MemoryWorkingSetBytes }

func detectOOM(in Input) []Finding {
	var out []Finding
	for _, s := range in.Latest {
		if s.LastTerminationReason != "OOMKilled" {
			continue
		}
		hist := in.HistoryOf(s)
		restarts := restartsWithin(hist, in.Now, in.Config.OOMWindow)
		// An OOM from hours ago that never came back is history, not a problem.
		if restarts == 0 && s.WaitingReason != "CrashLoopBackOff" {
			continue
		}
		sev := Warning
		if !s.Ready {
			sev = Critical
		}
		evidence := []string{"last container exit: OOMKilled (the kernel killed it for exceeding its memory limit)"}
		if s.MemoryLimitBytes > 0 {
			evidence = append(evidence, "memory limit: "+humanBytes(s.MemoryLimitBytes))
		}
		peakMem, havePeak := peak(hist, memoryWorkingSet)
		if havePeak {
			evidence = append(evidence, "highest memory seen before the kill: "+humanBytes(peakMem)+" (it grows fast right before an OOM, so the real peak was higher)")
		}
		if restarts > 0 {
			evidence = append(evidence, fmt.Sprintf("restarts in the last %s: %d", humanDur(in.Config.OOMWindow), restarts))
		}
		if s.WaitingReason != "" {
			evidence = append(evidence, "container now: "+s.WaitingReason)
		}

		fix := oomFix(s, peakMem)
		out = append(out, newFinding(OOMKilled, s, sev,
			fmt.Sprintf("%s is being killed for using too much memory (OOMKilled)", name(s)),
			"The model server tried to use more memory than its limit, so the kernel killed it and Kubernetes restarted it. "+
				"For LLM servers this usually means the context window (KV cache) or the number of parallel requests is too big for the memory given.",
			evidence, fix))
	}
	return out
}

func oomFix(s collector.Snapshot, peakMem float64) Fix {
	steps := []string{}
	switch s.Server {
	case "llamacpp":
		steps = append(steps,
			"KV cache memory grows with --ctx-size (shared by all --parallel slots). Lower --ctx-size, or lower --parallel.",
			"Or use a smaller quantization of the model (for example Q4 instead of Q8).")
	case "vllm":
		steps = append(steps,
			"Lower --max-model-len (KV cache per request) or --max-num-seqs (requests in a batch).",
			"If the GPU itself runs out of memory, that is a CUDA error in the logs, not OOMKilled: lower --gpu-memory-utilization.")
	default:
		steps = append(steps, "Reduce how much the server holds in memory: context length, batch size, or model size.")
	}
	fix := Fix{Summary: "Give it more memory, or make it need less."}
	if s.MemoryLimitBytes > 0 {
		want := max(s.MemoryLimitBytes*1.5, peakMem*1.3)
		q := memQuantity(want)
		fix.Steps = append([]string{fmt.Sprintf("Raise the memory limit from %s to about %s, and check the node has that much free.", humanBytes(s.MemoryLimitBytes), q)}, steps...)
		fix.Snippet = fmt.Sprintf("resources:\n  limits:\n    memory: %s", q)
	} else {
		fix.Steps = append([]string{"No memory limit is set, so the whole node ran out of memory. Set requests.memory to what the model really needs, so it lands on a node with room."}, steps...)
	}
	return fix
}

func detectContainerFailing(in Input) []Finding {
	var out []Finding
	for _, s := range in.Latest {
		if s.LastTerminationReason == "OOMKilled" {
			continue // the oom-killed detector explains this better
		}
		restarts := restartsWithin(in.HistoryOf(s), in.Now, in.Config.RestartWindow)
		sev := Warning
		if !s.Ready {
			sev = Critical
		}
		logs := fmt.Sprintf("kubectl -n %s logs %s --previous --tail=50", s.Namespace, s.Pod)

		var f Finding
		switch {
		case slices.Contains(pullReasons, s.WaitingReason):
			f = newFinding(ContainerFailing, s, sev,
				fmt.Sprintf("%s can't pull its container image (%s)", name(s), s.WaitingReason),
				"Kubernetes can't download the container image, so the model server never starts.",
				waitingEvidence(s, restarts),
				Fix{
					Summary: "Fix the image name or the registry access.",
					Steps: []string{
						"Check the image name and tag exist in the registry.",
						"For a private registry, check imagePullSecrets on the pod or its ServiceAccount.",
						"Check the node can reach the registry (network policy, proxy, rate limits).",
					},
					Snippet: fmt.Sprintf("kubectl -n %s describe pod %s", s.Namespace, s.Pod),
				})
		case slices.Contains(createReasons, s.WaitingReason):
			f = newFinding(ContainerFailing, s, sev,
				fmt.Sprintf("%s's container can't be created (%s)", name(s), s.WaitingReason),
				"The container can't start because something it needs is missing or wrong.",
				waitingEvidence(s, restarts),
				Fix{
					Summary: "Fix the missing piece named in the error.",
					Steps: []string{
						"Usually a ConfigMap, Secret or key it references doesn't exist, or a volume can't be mounted.",
						"The pod's Events show which one.",
					},
					Snippet: fmt.Sprintf("kubectl -n %s describe pod %s", s.Namespace, s.Pod),
				})
		case s.WaitingReason == "CrashLoopBackOff" || restarts >= in.Config.RestartThreshold:
			f = newFinding(ContainerFailing, s, sev,
				fmt.Sprintf("%s keeps crashing (%d restarts in %s)", name(s), restarts, humanDur(in.Config.RestartWindow)),
				"The model server starts and then exits again and again. Kubernetes waits longer between each restart (CrashLoopBackOff).",
				waitingEvidence(s, restarts),
				Fix{
					Summary: "Read the logs of the last crash; they say why.",
					Steps: []string{
						"Common causes for LLM servers: wrong model file path or name, a model format or architecture the server version doesn't support, a bad command-line flag, or a missing GPU driver.",
						"If it only crashes under load, check memory and the OOM detector.",
					},
					Snippet: logs,
				})
		default:
			continue
		}
		out = append(out, f)
	}
	return out
}

func waitingEvidence(s collector.Snapshot, restarts int32) []string {
	var ev []string
	if s.WaitingReason != "" {
		ev = append(ev, "container waiting: "+s.WaitingReason)
	}
	if s.LastTerminationReason != "" {
		ev = append(ev, "last container exit: "+s.LastTerminationReason)
	}
	ev = append(ev, fmt.Sprintf("restarts: %d total, %d recently", s.Restarts, restarts))
	if s.WarningReason != "" {
		ev = append(ev, fmt.Sprintf("latest warning: %s: %s", s.WarningReason, s.WarningMessage))
	}
	return ev
}
