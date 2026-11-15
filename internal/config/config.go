// Package config reads collector settings from flags, with environment
// variables as defaults. Flags win over env vars.
package config

import (
	"errors"
	"flag"
	"fmt"
	iofs "io/fs"
	"os"
	"strings"
	"time"
)

// Config holds the collector settings.
type Config struct {
	PrometheusURL string
	Interval      time.Duration
	QueryTimeout  time.Duration
	DBPath        string
	Retention     time.Duration
	Listen        string
	MappingFile   string
	Kubeconfig    string
	LogLevel      string

	Rules             bool
	PendingAfter      time.Duration
	RolloutStuckAfter time.Duration
	SlowStartAfter    time.Duration
	ResolveAfter      time.Duration

	Cost        bool
	PricingFile string

	Fixes              bool
	PolicyFile         string
	AdminPasswordFile  string
	ViewerPasswordFile string
	DashboardURL       string

	ClusterName      string
	Timezone         string
	SlackWebhookURL  string // a secret: never log it
	SlackWebhookFile string // file holding the URL (a mounted Secret); wins over SlackWebhookURL
	SlackMinSeverity string
	SlackCooldown    time.Duration
	SendTestAlert    bool
}

// Parse reads flags from args (usually os.Args[1:]).
func Parse(args []string) (*Config, error) {
	c := &Config{}
	fs := flag.NewFlagSet("sophron-collector", flag.ContinueOnError)
	fs.StringVar(&c.PrometheusURL, "prometheus-url", env("SOPHRON_PROMETHEUS_URL", "http://prometheus-operated.monitoring.svc:9090"), "Prometheus base URL")
	fs.DurationVar(&c.Interval, "interval", envDuration("SOPHRON_INTERVAL", 30*time.Second), "time between collection cycles")
	fs.DurationVar(&c.QueryTimeout, "query-timeout", envDuration("SOPHRON_QUERY_TIMEOUT", 10*time.Second), "timeout for one Prometheus query")
	fs.StringVar(&c.DBPath, "db", env("SOPHRON_DB", "/data/sophron.db"), "SQLite database file")
	fs.DurationVar(&c.Retention, "retention", envDuration("SOPHRON_RETENTION", 7*24*time.Hour), "how long to keep snapshots")
	fs.StringVar(&c.Listen, "listen", env("SOPHRON_LISTEN", ":8080"), "HTTP listen address")
	fs.StringVar(&c.MappingFile, "mapping-file", env("SOPHRON_MAPPING_FILE", ""), "metric mapping YAML (default: built in)")
	fs.StringVar(&c.Kubeconfig, "kubeconfig", env("KUBECONFIG", ""), "kubeconfig file (empty: in-cluster config)")
	fs.StringVar(&c.LogLevel, "log-level", env("SOPHRON_LOG_LEVEL", "info"), "debug, info, warn or error")
	fs.BoolVar(&c.Rules, "rules", envBool("SOPHRON_RULES", true), "run the rules engine after each cycle")
	fs.DurationVar(&c.PendingAfter, "pending-after", envDuration("SOPHRON_PENDING_AFTER", 2*time.Minute), "report pods unschedulable for this long")
	fs.DurationVar(&c.RolloutStuckAfter, "rollout-stuck-after", envDuration("SOPHRON_ROLLOUT_STUCK_AFTER", 3*time.Minute), "report rollouts whose new pod isn't Ready after this long")
	fs.DurationVar(&c.SlowStartAfter, "slow-start-after", envDuration("SOPHRON_SLOW_START_AFTER", 2*time.Minute), "report pods taking longer than this to become Ready")
	fs.DurationVar(&c.ResolveAfter, "resolve-after", envDuration("SOPHRON_RESOLVE_AFTER", 90*time.Second), "resolve a finding after it's gone for this long")
	fs.BoolVar(&c.Cost, "cost", envBool("SOPHRON_COST", true), "price every snapshot and serve the cost API")
	fs.StringVar(&c.PricingFile, "pricing-file", env("SOPHRON_PRICING_FILE", ""), "price file: node prices, owned hardware, reference APIs (empty: OpenCost or default prices)")
	fs.BoolVar(&c.Fixes, "fixes", envBool("SOPHRON_FIXES", true), "turn suggested actions into fix requests that can be approved")
	fs.StringVar(&c.PolicyFile, "policy-file", env("SOPHRON_POLICY_FILE", ""), "fix policy: auto/approve/never per action, allowed namespaces (empty: approve, no namespaces)")
	fs.StringVar(&c.AdminPasswordFile, "admin-password-file", env("SOPHRON_ADMIN_PASSWORD_FILE", "/etc/sophron/admin/password"), "dashboard admin password file (missing: read-only, no approvals)")
	fs.StringVar(&c.ViewerPasswordFile, "viewer-password-file", env("SOPHRON_VIEWER_PASSWORD_FILE", "/etc/sophron/admin/viewer-password"), "optional read-only viewer password file")
	fs.StringVar(&c.DashboardURL, "dashboard-url", env("SOPHRON_DASHBOARD_URL", ""), "dashboard address for links in alerts, e.g. http://localhost:8081")
	fs.StringVar(&c.ClusterName, "cluster-name", env("SOPHRON_CLUSTER_NAME", "kubernetes"), "cluster name shown in alerts")
	fs.StringVar(&c.Timezone, "timezone", env("SOPHRON_TIMEZONE", "UTC"), "time zone for times in alerts, e.g. Asia/Kolkata (UTC is shown too)")
	fs.StringVar(&c.SlackWebhookURL, "slack-webhook-url", env("SOPHRON_SLACK_WEBHOOK_URL", ""), "Slack incoming webhook URL; prefer the env var from a Secret (empty: Slack off)")
	fs.StringVar(&c.SlackWebhookFile, "slack-webhook-file", env("SOPHRON_SLACK_WEBHOOK_FILE", ""), "file containing the Slack webhook URL, e.g. a mounted Secret (missing file: Slack off)")
	fs.StringVar(&c.SlackMinSeverity, "slack-min-severity", env("SOPHRON_SLACK_MIN_SEVERITY", "warning"), "send findings at or above this severity: info, warning or critical")
	fs.DurationVar(&c.SlackCooldown, "slack-cooldown", envDuration("SOPHRON_SLACK_COOLDOWN", 10*time.Minute), "don't alert again for a problem that returns within this long")
	fs.BoolVar(&c.SendTestAlert, "send-test-alert", false, "send a sample alert to Slack and exit")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if c.Interval < 5*time.Second {
		return nil, fmt.Errorf("interval must be at least 5s, got %s", c.Interval)
	}
	if c.Retention < time.Hour {
		return nil, fmt.Errorf("retention must be at least 1h, got %s", c.Retention)
	}
	if c.SlackWebhookFile != "" {
		b, err := os.ReadFile(c.SlackWebhookFile)
		switch {
		case err == nil:
			c.SlackWebhookURL = strings.TrimSpace(string(b))
		case !errors.Is(err, iofs.ErrNotExist):
			return nil, fmt.Errorf("read slack webhook file: %w", err)
		}
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return nil, fmt.Errorf("unknown timezone %q", c.Timezone)
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	switch os.Getenv(key) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
