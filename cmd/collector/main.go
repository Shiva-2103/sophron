// Command sophron-collector reads model server metrics from Prometheus and
// pod, node and event facts from Kubernetes, and saves a snapshot per model
// pod on a fixed interval.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // time zones for alerts; the distroless image may not have them

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/Shiva-2103/sophron/internal/api"
	"github.com/Shiva-2103/sophron/internal/collector"
	"github.com/Shiva-2103/sophron/internal/config"
	"github.com/Shiva-2103/sophron/internal/cost"
	"github.com/Shiva-2103/sophron/internal/kube"
	"github.com/Shiva-2103/sophron/internal/mapping"
	"github.com/Shiva-2103/sophron/internal/notify"
	"github.com/Shiva-2103/sophron/internal/prom"
	"github.com/Shiva-2103/sophron/internal/rules"
	"github.com/Shiva-2103/sophron/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Parse(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return nil // --help printed the usage; that is not an error
	}
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel)

	slack, err := newSlack(cfg, log)
	if err != nil {
		return err
	}
	if cfg.SendTestAlert {
		return sendTestAlert(slack)
	}

	// Stop cleanly on Ctrl+C or when Kubernetes stops the pod (SIGTERM).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	m, err := mapping.Load(cfg.MappingFile)
	if err != nil {
		return err
	}
	promClient, err := prom.New(cfg.PrometheusURL, cfg.QueryTimeout)
	if err != nil {
		return err
	}
	restCfg, err := kubeConfig(cfg.Kubeconfig)
	if err != nil {
		return err
	}
	restCfg.UserAgent = "sophron-collector"
	clientset, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return err
	}
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if slack != nil {
		if err := slack.UseState(ctx, db); err != nil {
			log.Warn("could not load slack cooldown state", "error", err)
		}
	}

	inf := kube.NewInformers(clientset, 10*time.Minute)
	inf.Start(ctx)
	log.Info("waiting for Kubernetes caches to sync")
	if !inf.WaitForSync(ctx) {
		return errors.New("kubernetes caches did not sync")
	}

	c := &collector.Collector{Prom: promClient, Kube: inf, Mapping: m, Store: db, Log: log}
	apiSrv := &api.Server{Store: db, Status: c.Status, Synced: inf.HasSynced, Interval: cfg.Interval}

	if cfg.Cost {
		prices, err := cost.LoadPriceFile(cfg.PricingFile)
		if err != nil {
			return err
		}
		pricer := &cost.Pricer{Nodes: inf, Prom: promClient, File: prices, Log: log}
		c.Pricer = pricer
		apiSrv.Cost = &cost.Reporter{Store: db, Pricer: pricer, File: prices}
		log.Info("cost engine on", "priceRules", len(prices.Nodes), "referenceAPIs", len(prices.ReferenceAPIs))
	}

	if cfg.Rules {
		rc := rules.DefaultConfig()
		rc.PendingAfter, rc.RolloutStuckAfter, rc.SlowStartAfter = cfg.PendingAfter, cfg.RolloutStuckAfter, cfg.SlowStartAfter
		notifiers := []rules.Notifier{rules.LogNotifier{Log: log}}
		if slack != nil {
			go slack.Start(ctx)
			notifiers = append(notifiers, slack)
			log.Info("slack alerts on", "cluster", cfg.ClusterName, "minSeverity", cfg.SlackMinSeverity)
		} else {
			log.Info("slack alerts off (no webhook URL)")
		}
		engine := &rules.Engine{
			Store: db, Detectors: rules.All(), Config: rc, Log: log,
			Notifiers:    notifiers,
			Changes:      kube.Changes{Client: clientset},
			ResolveAfter: cfg.ResolveAfter,
		}
		c.AfterCycle = func(ctx context.Context, snaps []collector.Snapshot) {
			st, err := engine.Process(ctx, snaps)
			if err != nil {
				log.Error("rules engine failed", "error", err)
				return
			}
			log.Info("rules engine done", "open", st.Open, "opened", st.Opened, "resolved", st.Resolved)
		}
		apiSrv.Findings, apiSrv.Rules, apiSrv.Detectors = db, engine.Status, engine.Detectors
		log.Info("rules engine on", "detectors", len(engine.Detectors))
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           apiSrv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info("API listening", "addr", cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("API server stopped", "error", err)
			stop()
		}
	}()

	go prune(ctx, db, cfg.Retention, log)

	log.Info("collector started", "prometheus", cfg.PrometheusURL, "interval", cfg.Interval, "servers", len(m.Servers))
	c.Run(ctx, cfg.Interval)

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// newSlack returns the Slack notifier, or nil when no webhook is set.
func newSlack(cfg *config.Config, log *slog.Logger) (*notify.Slack, error) {
	if cfg.SlackWebhookURL == "" {
		return nil, nil
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		return nil, err
	}
	return notify.NewSlack(notify.SlackConfig{
		WebhookURL:  cfg.SlackWebhookURL,
		Cluster:     cfg.ClusterName,
		MinSeverity: rules.Severity(strings.ToLower(cfg.SlackMinSeverity)),
		Location:    loc,
		Cooldown:    cfg.SlackCooldown,
	}, log)
}

// sendTestAlert posts a sample finding, so you can check the webhook and
// see what an alert looks like.
func sendTestAlert(slack *notify.Slack) error {
	if slack == nil {
		return errors.New("no Slack webhook URL: set SOPHRON_SLACK_WEBHOOK_URL (make slack-secret)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f := notify.SampleFinding(time.Now().UTC())
	if err := slack.Send(ctx, rules.Event{Type: notify.EventTest, Finding: f}); err != nil {
		return err
	}
	fmt.Println("test alert sent: check your Slack channel")
	return nil
}

// kubeConfig uses the pod's service account inside a cluster, or a
// kubeconfig file when running on a laptop.
func kubeConfig(path string) (*rest.Config, error) {
	if path == "" {
		if c, err := rest.InClusterConfig(); err == nil {
			return c, nil
		}
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules.ExplicitPath = path
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, nil).ClientConfig()
}

func prune(ctx context.Context, db *store.Store, retention time.Duration, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := db.Prune(ctx, retention); err != nil {
			log.Warn("prune failed", "error", err)
		} else if n > 0 {
			log.Info("pruned old snapshots", "deleted", n)
		}
		if err := db.PruneAlertMarks(ctx, 24*time.Hour); err != nil {
			log.Warn("prune alert marks failed", "error", err)
		}
		if n, err := db.PruneFindings(ctx, retention); err != nil {
			log.Warn("prune findings failed", "error", err)
		} else if n > 0 {
			log.Info("pruned old findings", "deleted", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
