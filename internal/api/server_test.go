package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Shiva-2103/sophron/internal/auth"
	"github.com/Shiva-2103/sophron/internal/collector"
	"github.com/Shiva-2103/sophron/internal/cost"
	"github.com/Shiva-2103/sophron/internal/fixes"
	"github.com/Shiva-2103/sophron/internal/rules"
)

type fakeStore struct {
	snaps   []collector.Snapshot
	pingErr error
	gotPod  string
}

func (f *fakeStore) Latest(context.Context, time.Duration) ([]collector.Snapshot, error) {
	return f.snaps, nil
}
func (f *fakeStore) History(_ context.Context, _, pod string, _ time.Time) ([]collector.Snapshot, error) {
	f.gotPod = pod
	return f.snaps, nil
}
func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func do(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestReadyz(t *testing.T) {
	st := collector.Status{}
	synced := false
	s := &Server{Store: &fakeStore{}, Interval: 30 * time.Second,
		Status: func() collector.Status { return st }, Synced: func() bool { return synced }}
	h := s.Handler()

	if do(t, h, "/readyz").Code != 503 {
		t.Error("not ready before informers sync")
	}
	synced = true
	if do(t, h, "/readyz").Code != 503 {
		t.Error("not ready before the first cycle")
	}
	st = collector.Status{LastRun: time.Now()}
	if c := do(t, h, "/readyz").Code; c != 200 {
		t.Errorf("ready after a good cycle, got %d", c)
	}
	st = collector.Status{LastRun: time.Now(), Error: "boom"}
	if do(t, h, "/readyz").Code != 503 {
		t.Error("not ready after a failed cycle")
	}
	st = collector.Status{LastRun: time.Now().Add(-5 * time.Minute)}
	if do(t, h, "/readyz").Code != 503 {
		t.Error("not ready when cycles stopped")
	}
	s.Store = &fakeStore{pingErr: errors.New("disk full")}
	st = collector.Status{LastRun: time.Now()}
	if do(t, s.Handler(), "/readyz").Code != 503 {
		t.Error("not ready when the database fails")
	}
}

func TestLatestAndHistory(t *testing.T) {
	fs := &fakeStore{}
	h := (&Server{Store: fs, Interval: time.Second, Status: func() collector.Status { return collector.Status{} }}).Handler()

	rec := do(t, h, "/api/v1/snapshots/latest")
	if rec.Code != 200 || rec.Body.String() != "[]\n" {
		t.Errorf("empty latest should be [], got %d %q", rec.Code, rec.Body.String())
	}

	fs.snaps = []collector.Snapshot{{Namespace: "llm", Pod: "a"}}
	rec = do(t, h, "/api/v1/snapshots?namespace=llm&pod=a&since=1h")
	var got []collector.Snapshot
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || len(got) != 1 || fs.gotPod != "a" {
		t.Errorf("history: %d %s", rec.Code, rec.Body.String())
	}
	if do(t, h, "/api/v1/snapshots?namespace=llm").Code != 400 {
		t.Error("pod is required")
	}
	if do(t, h, "/api/v1/snapshots?namespace=llm&pod=a&since=forever").Code != 400 {
		t.Error("bad since should be rejected")
	}
	if do(t, h, "/healthz").Code != 200 {
		t.Error("healthz")
	}
}

type fakeFindings struct{ gotStatus string }

func (f *fakeFindings) Findings(_ context.Context, status string, _ int) ([]rules.Finding, error) {
	f.gotStatus = status
	return []rules.Finding{{Key: "oom-killed/llm/llm-server"}}, nil
}

func TestFindingsAPI(t *testing.T) {
	ff := &fakeFindings{}
	s := &Server{Store: &fakeStore{}, Interval: time.Second, Status: func() collector.Status { return collector.Status{} }}
	if do(t, s.Handler(), "/api/v1/findings").Code != 404 {
		t.Error("findings should 404 when the rules engine is off")
	}
	s.Findings, s.Detectors = ff, rules.All()
	s.Rules = func() rules.Status { return rules.Status{Open: 1} }
	h := s.Handler()
	if rec := do(t, h, "/api/v1/findings"); rec.Code != 200 || ff.gotStatus != "open" {
		t.Errorf("default should be open: %d %q", rec.Code, ff.gotStatus)
	}
	if do(t, h, "/api/v1/findings?status=all"); ff.gotStatus != "" {
		t.Errorf("all should not filter, got %q", ff.gotStatus)
	}
	if do(t, h, "/api/v1/findings?status=nope").Code != 400 {
		t.Error("bad status should be rejected")
	}
	var ds []map[string]string
	rec := do(t, h, "/api/v1/detectors")
	if json.Unmarshal(rec.Body.Bytes(), &ds) != nil || len(ds) != 6 {
		t.Errorf("detectors: %s", rec.Body.String())
	}
	if do(t, h, "/api/v1/rules/status").Code != 200 {
		t.Error("rules status")
	}
}

type fakeCost struct{ window time.Duration }

func (f *fakeCost) Models(_ context.Context, w time.Duration) ([]cost.ModelCost, error) {
	f.window = w
	return nil, nil
}
func (f *fakeCost) Nodes(context.Context) ([]cost.NodeCost, error) { return nil, nil }
func (f *fakeCost) Savings(context.Context, time.Duration) ([]cost.Saving, error) {
	return []cost.Saving{}, nil
}
func (f *fakeCost) Trend(context.Context, time.Duration) ([]cost.TrendPoint, error) {
	return []cost.TrendPoint{}, nil
}
func (f *fakeCost) Summary(_ context.Context, w time.Duration) (cost.Summary, error) {
	f.window = w
	return cost.Summary{SpendPerHour: 0.33}, nil
}

func TestCostAPI(t *testing.T) {
	s := &Server{Store: &fakeStore{}, Interval: time.Second, Status: func() collector.Status { return collector.Status{} }}
	if do(t, s.Handler(), "/api/v1/cost/summary").Code != 404 {
		t.Error("cost should 404 when the cost engine is off")
	}
	fc := &fakeCost{}
	s.Cost = fc
	h := s.Handler()
	if rec := do(t, h, "/api/v1/cost/summary"); rec.Code != 200 || fc.window != time.Hour {
		t.Errorf("summary: %d, window %s", rec.Code, fc.window)
	}
	if rec := do(t, h, "/api/v1/cost/models?window=24h"); rec.Code != 200 || fc.window != 24*time.Hour || rec.Body.String() != "[]\n" {
		t.Errorf("models: %d %q window %s", rec.Code, rec.Body.String(), fc.window)
	}
	if do(t, h, "/api/v1/cost/nodes").Code != 200 {
		t.Error("nodes")
	}
	if do(t, h, "/api/v1/cost/models?window=1s").Code != 400 {
		t.Error("a 1s window should be rejected")
	}
}

type fakeDecider struct{ approved []int64 }

func (f *fakeDecider) Approve(_ context.Context, id int64, _, _ string) (fixes.Request, error) {
	f.approved = append(f.approved, id)
	return fixes.Request{ID: id, Status: fixes.StatusApplied}, nil
}
func (f *fakeDecider) Deny(_ context.Context, id int64, _, _ string) (fixes.Request, error) {
	return fixes.Request{ID: id, Status: fixes.StatusDenied}, nil
}

func TestAuthAndApprovals(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(dir+"/admin", []byte("admin-password-1"), 0o600)
	_ = os.WriteFile(dir+"/viewer", []byte("viewer-password-1"), 0o600)
	a, _ := auth.New(auth.Config{AdminPasswordFile: dir + "/admin", ViewerPasswordFile: dir + "/viewer"})
	fd := &fakeDecider{}
	h := (&Server{Store: &fakeStore{}, Interval: time.Second, Status: func() collector.Status { return collector.Status{} },
		Auth: a, FixDecider: fd}).Handler()

	call := func(method, path, user, pw string, csrf bool) int {
		r := httptest.NewRequest(method, path, strings.NewReader(`{"reason":"ok"}`))
		if user != "" {
			r.SetBasicAuth(user, pw)
		}
		if csrf {
			r.Header.Set("X-Sophron", "1")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	if c := call("GET", "/api/v1/status", "", "", false); c != 401 {
		t.Errorf("no login: %d, want 401", c)
	}
	if c := call("GET", "/api/v1/me", "", "", false); c != 200 {
		t.Errorf("/me must stay open: %d", c)
	}
	if c := call("GET", "/api/v1/status", "viewer", "viewer-password-1", false); c != 200 {
		t.Errorf("viewer read: %d", c)
	}
	if c := call("POST", "/api/v1/fixes/7/approve", "viewer", "viewer-password-1", true); c != 403 {
		t.Errorf("viewer approve: %d, want 403", c)
	}
	if c := call("POST", "/api/v1/fixes/7/approve", "admin", "admin-password-1", true); c != 200 || len(fd.approved) != 1 {
		t.Errorf("admin approve: %d %v", c, fd.approved)
	}
	// A cookie session without the custom header looks like a cross-site form: refused.
	rec := httptest.NewRecorder()
	_, _ = a.Login(rec, httptest.NewRequest("POST", "/", nil), "admin", "admin-password-1")
	r := httptest.NewRequest("POST", "/api/v1/fixes/7/approve", nil)
	r.AddCookie(rec.Result().Cookies()[0])
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	if out.Code != 403 || len(fd.approved) != 1 {
		t.Errorf("CSRF: %d, approvals %v", out.Code, fd.approved)
	}
	if h := out.Header().Get("Content-Security-Policy"); !strings.Contains(h, "default-src 'self'") {
		t.Errorf("missing CSP: %q", h)
	}
}
