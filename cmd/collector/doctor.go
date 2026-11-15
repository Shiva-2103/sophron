package main

import (
	"context"
	"fmt"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/Shiva-2103/sophron/internal/api"
	"github.com/Shiva-2103/sophron/internal/auth"
	"github.com/Shiva-2103/sophron/internal/collector"
	"github.com/Shiva-2103/sophron/internal/config"
	"github.com/Shiva-2103/sophron/internal/fixes"
	"github.com/Shiva-2103/sophron/internal/notify"
	"github.com/Shiva-2103/sophron/internal/prom"
	"github.com/Shiva-2103/sophron/internal/store"
)

// doctor returns the setup checks behind the dashboard's Setup page and
// `make doctor`: each says what's wrong and the exact fix.
func doctor(cfg *config.Config, p prom.Querier, c *collector.Collector, db *store.Store, k kubernetes.Interface,
	authn *auth.Auth, slack *notify.Slack, policy fixes.Policy) func(context.Context) []api.Check {
	return func(ctx context.Context) []api.Check {
		var out []api.Check
		add := func(name, status, detail, fix string) {
			out = append(out, api.Check{Name: name, Status: status, Detail: detail, Fix: fix})
		}

		if _, err := p.Query(ctx, "up", time.Now()); err != nil {
			add("Prometheus", "fail", "can't query "+cfg.PrometheusURL+": "+err.Error(), "Set --prometheus-url to your Prometheus service, e.g. http://prometheus-operated.monitoring.svc:9090")
		} else {
			add("Prometheus", "ok", "reachable at "+cfg.PrometheusURL, "")
		}

		st := c.Status()
		switch {
		case st.Error != "":
			add("Collection", "fail", "last cycle failed: "+st.Error, "See the collector logs")
		case st.Pods == 0:
			add("Model servers", "warn", "no model server pods found", "Label your model pods sophron.dev/model-server=vllm (or llamacpp), or make sure their metrics reach Prometheus")
		default:
			add("Model servers", "ok", fmt.Sprintf("%d model pods, last cycle %s ago", st.Pods, time.Since(st.LastRun).Round(time.Second)), "")
		}

		latest, _ := db.Latest(ctx, 10*time.Minute)
		var tokens, cpu, gpuWanted, gpuSeen int
		for _, s := range latest {
			if s.GenerationTokensPerSecond != nil {
				tokens++
			}
			if s.CPUCores != nil {
				cpu++
			}
			if s.GPURequest > 0 {
				gpuWanted++
				if s.GPUUtilizationRatio != nil {
					gpuSeen++
				}
			}
		}
		switch {
		case len(latest) == 0:
			add("Token metrics", "skip", "no snapshots yet", "")
		case tokens == 0:
			add("Token metrics", "fail", "no model pod reports tokens, so there is no cost per token", "Add a ServiceMonitor or PodMonitor for your model server's /metrics (llama.cpp needs --metrics)")
		default:
			add("Token metrics", "ok", fmt.Sprintf("%d of %d pods report tokens", tokens, len(latest)), "")
		}
		if len(latest) > 0 && cpu == 0 {
			add("Container metrics", "fail", "no CPU or memory usage from cAdvisor", "Make sure Prometheus scrapes the kubelet's /metrics/cadvisor (kube-prometheus-stack does)")
		} else if len(latest) > 0 {
			add("Container metrics", "ok", "CPU and memory usage found", "")
		}
		switch {
		case gpuWanted == 0:
			add("GPU metrics", "skip", "no model pod uses a GPU", "")
		case gpuSeen == 0:
			add("GPU metrics", "warn", "GPU pods found, but no GPU usage", "Install NVIDIA's DCGM exporter and let Prometheus scrape it")
		default:
			add("GPU metrics", "ok", fmt.Sprintf("%d of %d GPU pods report usage", gpuSeen, gpuWanted), "")
		}

		priced, estimated := 0, 0
		for _, s := range latest {
			if s.CostPerHour != nil {
				priced++
				if s.PriceSource == "default prices" {
					estimated++
				}
			}
		}
		switch {
		case !cfg.Cost:
			add("Prices", "skip", "cost engine is off", "")
		case priced == 0:
			add("Prices", "warn", "no priced snapshots yet", "Wait one cycle")
		case estimated > 0:
			add("Prices", "warn", fmt.Sprintf("%d pods priced with default prices (an estimate)", estimated), "Add a price file rule, a sophron.dev/hourly-price node label, or install OpenCost")
		default:
			add("Prices", "ok", "every pod has a real price", "")
		}

		if slack == nil {
			add("Slack", "warn", "alerts go to the log only", "make slack-secret")
		} else {
			add("Slack", "ok", "alerts go to Slack", "")
		}
		if authn.Enabled() {
			add("Login", "ok", "admin sign-in is on", "")
		} else {
			add("Login", "warn", "no admin password: the dashboard is read-only and fixes can't be approved", "make admin-password")
		}

		switch {
		case !cfg.Fixes:
			add("Fixes", "skip", "fixes are off (--fixes=false)", "")
		case len(policy.Namespaces) == 0:
			add("Fixes", "warn", "the policy allows no namespaces, so every fix is blocked", "List namespaces in the sophron-policy ConfigMap")
		default:
			for _, ns := range policy.Namespaces {
				if ns == "*" {
					add("Fixes", "warn", "the policy allows every namespace", "List only the namespaces Sophron may change")
					continue
				}
				ok := canUpdate(ctx, k, ns)
				if ok {
					add("Fixes in "+ns, "ok", "allowed by policy and RBAC (default mode: "+policy.Default+")", "")
				} else {
					add("Fixes in "+ns, "fail", "the policy allows "+ns+" but RBAC doesn't let Sophron update Deployments there", "Apply the sophron-fixer Role and RoleBinding in "+ns)
				}
			}
		}

		if bad, err := db.VerifyAudit(ctx); err != nil {
			add("Audit log", "fail", err.Error(), "")
		} else if bad != 0 {
			add("Audit log", "fail", fmt.Sprintf("the hash chain breaks at entry %d: the log was changed", bad), "Investigate who edited the database")
		} else {
			add("Audit log", "ok", "hash chain intact", "")
		}
		return out
	}
}

func canUpdate(ctx context.Context, k kubernetes.Interface, ns string) bool {
	r, err := k.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{
			Namespace: ns, Verb: "update", Group: "apps", Resource: "deployments"}},
	}, metav1.CreateOptions{})
	return err == nil && r.Status.Allowed
}
