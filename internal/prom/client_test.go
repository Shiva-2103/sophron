package prom

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func server(t *testing.T, status int, body string) *Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("query") == "" {
			t.Errorf("query missing from form: %v", err)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	c, err := New(ts.URL, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestQueryVector(t *testing.T) {
	c := server(t, 200, `{"status":"success","data":{"resultType":"vector","result":[
		{"metric":{"namespace":"llm","pod":"a"},"value":[1700000000.1,"48.1"]},
		{"metric":{"namespace":"llm","pod":"b"},"value":[1700000000.1,"NaN"]}]}}`)
	got, err := c.Query(context.Background(), "up", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Labels["pod"] != "a" || got[0].Value != 48.1 {
		t.Fatalf("unexpected result: %+v", got)
	}
	if got[1].Value == got[1].Value { // NaN is the only value not equal to itself
		t.Fatalf("expected NaN, got %v", got[1].Value)
	}
}

func TestQueryPrometheusError(t *testing.T) {
	c := server(t, 400, `{"status":"error","errorType":"bad_data","error":"parse error"}`)
	if _, err := c.Query(context.Background(), "up{", time.Time{}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestQueryNonJSON(t *testing.T) {
	c := server(t, 502, `<html>bad gateway</html>`)
	if _, err := c.Query(context.Background(), "up", time.Time{}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestQueryWrongResultType(t *testing.T) {
	c := server(t, 200, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
	if _, err := c.Query(context.Background(), "up[5m]", time.Time{}); err == nil {
		t.Fatal("expected an error for a matrix result")
	}
}

func TestNewRejectsBadURL(t *testing.T) {
	for _, u := range []string{"", "prometheus:9090", "://x"} {
		if _, err := New(u, time.Second); err == nil {
			t.Errorf("expected error for %q", u)
		}
	}
}
