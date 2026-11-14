package rules

import (
	"fmt"
	"slices"

	"github.com/Shiva-2103/sophron/internal/collector"
)

func detectSlowStart(in Input) []Finding {
	var out []Finding
	limit := in.Config.SlowStartAfter
	for _, s := range in.Latest {
		fix := slowStartFix(s)
		switch {
		// Still starting, and already slow. Crashes and pull errors are
		// other detectors' business, so only plain "starting" pods count.
		case !s.Ready && !unscheduled(s) && startingNormally(s) && age(in.Now, s.CreatedAt) >= limit:
			took := age(in.Now, s.CreatedAt)
			out = append(out, newFinding(SlowColdStart, s, Warning,
				fmt.Sprintf("%s is still starting after %s", name(s), humanDur(took)),
				fmt.Sprintf("Pod %s was created %s ago and is not Ready yet. Until it is, it serves nothing; if a node fails, recovery takes at least this long.", s.Pod, humanDur(took)),
				[]string{
					fmt.Sprintf("created %s ago, not Ready", humanDur(took)),
					"phase: " + s.Phase,
					fmt.Sprintf("threshold: %s", humanDur(limit)),
				}, fix))

		// Finished starting recently, but it took too long.
		case s.Ready && s.StartupSeconds != nil && *s.StartupSeconds >= limit.Seconds() &&
			age(in.Now, s.ReadyAt) <= in.Config.SlowStartRecent:
			took := humanDur(secondsToDur(*s.StartupSeconds))
			out = append(out, newFinding(SlowColdStart, s, Info,
				fmt.Sprintf("%s took %s to start", name(s), took),
				"The model server is serving now, but it took a long time to become Ready. Every restart, scale-up and node failure costs this much time without serving.",
				[]string{
					"startup (pod start to Ready): " + took,
					fmt.Sprintf("threshold: %s", humanDur(limit)),
				}, fix))
		}
	}
	return out
}

func startingNormally(s collector.Snapshot) bool {
	return s.Restarts == 0 && slices.Contains([]string{"", "PodInitializing", "ContainerCreating"}, s.WaitingReason)
}

func slowStartFix(s collector.Snapshot) Fix {
	return Fix{
		Summary: "Make the model ready to load before the pod starts.",
		Steps: []string{
			"Downloading the model on every start is the usual cause. Keep it on a PersistentVolume, a node-local cache, or bake it into the image.",
			"Large images take long to pull. Pre-pull them on the nodes, or slim the image.",
			"Loading from slow disk adds time. Use a faster StorageClass for the model volume.",
			"Make sure the startupProbe allows this long, or Kubernetes will kill the pod before it's ready.",
		},
		Snippet: fmt.Sprintf("kubectl -n %s describe pod %s   # Events show where the time went", s.Namespace, s.Pod),
	}
}

func requestsWaiting(s collector.Snapshot) bool {
	return s.RequestsWaiting != nil && *s.RequestsWaiting >= 1
}

func detectQueueBacklog(in Input) []Finding {
	var out []Finding
	need := max(in.Config.QueueCycles, 1)
	for _, s := range in.Latest {
		hist := in.HistoryOf(s)
		if trailing(hist, requestsWaiting) < need {
			continue
		}
		waiting := *s.RequestsWaiting
		running := 0.0
		if s.RequestsRunning != nil {
			running = *s.RequestsRunning
		}
		evidence := []string{
			fmt.Sprintf("requests waiting for a free slot: %.0f (for the last %d snapshots)", waiting, need),
			fmt.Sprintf("requests being served: %.0f", running),
		}
		if s.GenerationTokensPerSecond != nil {
			evidence = append(evidence, fmt.Sprintf("generating %.1f tokens/s", *s.GenerationTokensPerSecond))
		}

		// One metric can mislead: before saying "add slots", check whether
		// the hardware is already maxed out.
		saturated := ""
		if s.CPUCores != nil && s.CPULimitCores > 0 && *s.CPUCores >= 0.9*s.CPULimitCores {
			saturated = fmt.Sprintf("CPU is at its limit (%s of %s cores)", cores(*s.CPUCores), cores(s.CPULimitCores))
		}
		if s.GPUUtilizationRatio != nil && *s.GPUUtilizationRatio >= 0.9 {
			saturated = fmt.Sprintf("GPU is %.0f%% busy", *s.GPUUtilizationRatio*100)
		}
		if saturated != "" {
			evidence = append(evidence, saturated)
		}

		var fix Fix
		if saturated != "" {
			fix = Fix{
				Summary: "The hardware is already busy. Add capacity to cut waiting.",
				Steps: []string{
					saturated + ". More parallel slots would share the same hardware: batching may raise total throughput a little, but each request gets slower.",
					"To really cut waiting: add a replica, move to a bigger node, or use a GPU.",
				},
				Snippet: fmt.Sprintf("kubectl -n %s scale %s --replicas=2", s.Namespace, kubectlTarget(s)),
			}
		} else {
			fix = Fix{Summary: "Allow more requests at once, or add a replica."}
			switch s.Server {
			case "llamacpp":
				fix.Steps = []string{
					fmt.Sprintf("Raise --parallel (about %.0f slots are in use). --ctx-size is shared by all slots, so raise it too to keep the same context per request.", running),
					"Or add a replica.",
				}
			case "vllm":
				fix.Steps = []string{"Raise --max-num-seqs if GPU memory allows, or add a replica."}
			default:
				fix.Steps = []string{"Raise the server's concurrency setting, or add a replica."}
			}
		}
		out = append(out, newFinding(QueueBacklog, s, Warning,
			fmt.Sprintf("Requests are queuing on %s: %.0f waiting, %.0f being served", name(s), waiting, running),
			"Every serving slot has been busy for several snapshots in a row. New requests wait before the model even starts on them, which users feel as extra latency.",
			evidence, fix))
	}
	return out
}
