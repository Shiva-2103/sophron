package config

import (
	"os"
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

func TestSlackWebhookFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/webhook-url"
	if err := os.WriteFile(path, []byte("https://hooks.slack.com/services/T/B/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Parse([]string{"--slack-webhook-file=" + path})
	if err != nil || c.SlackWebhookURL != "https://hooks.slack.com/services/T/B/x" {
		t.Fatalf("url %q, err %v", c.SlackWebhookURL, err)
	}
	// No Secret mounted: Slack is simply off.
	c, err = Parse([]string{"--slack-webhook-file=" + dir + "/missing"})
	if err != nil || c.SlackWebhookURL != "" {
		t.Fatalf("missing file: url %q, err %v", c.SlackWebhookURL, err)
	}
}
