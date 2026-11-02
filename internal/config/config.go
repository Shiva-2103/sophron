// Package config reads collector settings from flags, with environment
// variables as defaults. Flags win over env vars.
package config

import (
	"flag"
	"fmt"
	"os"
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
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if c.Interval < 5*time.Second {
		return nil, fmt.Errorf("interval must be at least 5s, got %s", c.Interval)
	}
	if c.Retention < time.Hour {
		return nil, fmt.Errorf("retention must be at least 1h, got %s", c.Retention)
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
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
