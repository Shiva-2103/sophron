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
