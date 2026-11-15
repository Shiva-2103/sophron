package config

import (
	"testing"
	"time"
)

func TestDefaultsAndFlags(t *testing.T) {
	t.Setenv("SOPHRON_INTERVAL", "45s")
	c, err := Parse([]string{"--prometheus-url", "http://p:9090"})
	if err != nil {
		t.Fatal(err)
	}
	if c.PrometheusURL != "http://p:9090" || c.Interval != 45*time.Second || c.DBPath != "/data/sophron.db" {
		t.Errorf("unexpected config: %+v", c)
	}
}

func TestRejectsTooShortInterval(t *testing.T) {
	if _, err := Parse([]string{"--interval", "1s"}); err == nil {
		t.Error("expected error")
	}
	if _, err := Parse([]string{"--retention", "10m"}); err == nil {
		t.Error("expected error")
	}
}

func TestSlackSettings(t *testing.T) {
	t.Setenv("SOPHRON_SLACK_WEBHOOK_URL", "https://hooks.slack.com/services/T/B/x")
	c, err := Parse([]string{"--timezone=Asia/Kolkata", "--cluster-name=prod-eu"})
	if err != nil {
		t.Fatal(err)
	}
	if c.SlackWebhookURL == "" || c.ClusterName != "prod-eu" || c.SlackMinSeverity != "warning" {
		t.Errorf("unexpected config: cluster %q, min %q, url set %v", c.ClusterName, c.SlackMinSeverity, c.SlackWebhookURL != "")
	}
	if _, err := Parse([]string{"--timezone=Mars/Olympus"}); err == nil {
		t.Error("want an error for an unknown time zone")
	}
}
