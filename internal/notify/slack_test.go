package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shiva-2103/sophron/internal/rules"
)

var t0 = time.Date(2026, 11, 15, 7, 50, 0, 0, time.UTC)

// fakeSlack records posted bodies and answers with the given status codes in turn.
type fakeSlack struct {
	mu     sync.Mutex
	bodies []string
	codes  []int
	header http.Header
}

func (f *fakeSlack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies = append(f.bodies, string(b))
	f.header = r.Header.Clone()
	code := http.StatusOK
	if len(f.codes) > 0 {
		code, f.codes = f.codes[0], f.codes[1:]
	}
	if code == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "2")
	}
	w.WriteHeader(code)
	if code == http.StatusOK {
		_, _ = io.WriteString(w, "ok")
	} else {
		_, _ = io.WriteString(w, "invalid_payload")
	}
}

func (f *fakeSlack) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func newTestSlack(t *testing.T, url string) (*Slack, *[]time.Duration) {
	t.Helper()
	ist, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSlack(SlackConfig{WebhookURL: url, Cluster: "sophron-lab", Location: ist},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var slept []time.Duration
	s.now = func() time.Time { return t0 }
	s.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	return s, &slept
}

func opened(f rules.Finding) rules.Event { return rules.Event{Type: rules.EventOpened, Finding: f} }

func TestMessageLayout(t *testing.T) {
	s, _ := newTestSlack(t, "https://hooks.slack.com/services/T/B/x")
	f := SampleFinding(t0)
	f.ID = 12
	m := s.Message(opened(f))

	if !strings.HasPrefix(m.Text, "[CRITICAL] sophron-lab | llm/llm-server | ") {
		t.Errorf("fallback text = %q", m.Text)
	}
	if len(m.Blocks) != 1 || m.Blocks[0].Type != "header" {
		t.Fatalf("want one header block, got %+v", m.Blocks)
	}
	if h := m.Blocks[0].Text.Text; !strings.HasPrefix(h, "🔴 CRITICAL · llm/llm-server is being killed") {
		t.Errorf("header = %q", h)
	}
	if len(m.Attachments) != 1 || m.Attachments[0].Color != colorCritical {
		t.Fatalf("want one red attachment, got %+v", m.Attachments)
	}
	all := dump(t, m)
	for _, want := range []string{
		"*Status*\\nOpen", "*Cluster*\\nsophron-lab", "`deployment/llm-server`",
		"15 Nov 2026, 01:20 PM IST (07:50 UTC)",
		"*What's wrong*", "*Evidence*", "• last exit: OOMKilled",
		"*Suggested fix*", "1. Raise the memory limit", "```kubectl -n llm set resources",
		"finding #12", "detector `oom-killed`",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("message missing %q", want)
		}
	}
}

func TestResolvedMessage(t *testing.T) {
	s, _ := newTestSlack(t, "https://hooks.slack.com/services/T/B/x")
	f := SampleFinding(t0.Add(-6*time.Minute - 30*time.Second))
	f.Status, f.ResolvedAt = rules.StatusResolved, t0
	f.Resolution = &rules.Resolution{
		Summary:   "Fixed by a change to the workload.",
		Changes:   []string{"memory limit 512Mi → 6Gi", "1 of 1 pods Ready now"},
		ChangedBy: "kubectl apply", ChangedAt: t0.Add(-2 * time.Minute),
		DownSeconds: 240, DownCost: func() *float64 { v := 0.04; return &v }(),
	}
	m := s.Message(rules.Event{Type: rules.EventResolved, Finding: f})

	if m.Attachments[0].Color != colorResolved {
		t.Errorf("color = %s, want green", m.Attachments[0].Color)
	}
	if h := m.Blocks[0].Text.Text; !strings.HasPrefix(h, "✅ RESOLVED · ") {
		t.Errorf("header = %q", h)
	}
	all := dump(t, m)
	if !strings.Contains(all, "*Open for*\\n6m 30s") {
		t.Errorf("resolved message should say how long it was open: %s", all)
	}
	for _, want := range []string{"*How it was resolved*\\nFixed by a change", "• memory limit 512Mi → 6Gi",
		"*Changed with*\\n`kubectl apply`\\n15 Nov 2026, 01:18 PM IST", "*Approved by*\\nNot needed",
		"*Not serving for*\\n4m 0s", "*Cost while down*\\n$0.04"} {
		if !strings.Contains(all, want) {
			t.Errorf("resolved message missing %q", want)
		}
	}
	if strings.Contains(all, "Suggested fix") {
		t.Error("resolved message should be short, without the fix")
	}
}

func TestEscalatedAndTestMessages(t *testing.T) {
	s, _ := newTestSlack(t, "https://hooks.slack.com/services/T/B/x")
	f := SampleFinding(t0)
	if h := s.Message(rules.Event{Type: rules.EventEscalate, Finding: f}).Blocks[0].Text.Text; !strings.HasPrefix(h, "⏫ ESCALATED TO CRITICAL · ") {
		t.Errorf("escalated header = %q", h)
	}
	m := s.Message(rules.Event{Type: EventTest, Finding: f})
	if m.Attachments[0].Color != colorTest || !strings.HasPrefix(m.Text, "[TEST]") {
		t.Errorf("test message: color %s, text %q", m.Attachments[0].Color, m.Text)
	}
}

func TestEscapingAndLimits(t *testing.T) {
	s, _ := newTestSlack(t, "https://hooks.slack.com/services/T/B/x")
	f := SampleFinding(t0)
	f.Title = strings.Repeat("x", 300)
	f.Summary = "a <!channel> & <https://evil|click>"
	f.Fix.Snippet = strings.Repeat("y", 5000)
	m := s.Message(opened(f))

	if n := len([]rune(m.Blocks[0].Text.Text)); n > headerLimit {
		t.Errorf("header is %d chars, limit %d", n, headerLimit)
	}
	all := dump(t, m)
	if strings.Contains(all, "<!channel>") || strings.Contains(all, "<https://evil") {
		t.Error("mrkdwn control characters were not escaped")
	}
	if !strings.Contains(all, "&lt;!channel&gt; &amp;") {
		t.Errorf("escaped summary missing: %s", all)
	}
	for _, b := range m.Attachments[0].Blocks {
		if b.Text != nil && len([]rune(b.Text.Text)) > sectionLimit {
			t.Errorf("section is %d chars, limit %d", len([]rune(b.Text.Text)), sectionLimit)
		}
	}
}

func TestSendRetriesThenSucceeds(t *testing.T) {
	fs := &fakeSlack{codes: []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusOK}}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	s, slept := newTestSlack(t, srv.URL)

	if err := s.Send(context.Background(), opened(SampleFinding(t0))); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if fs.calls() != 3 {
		t.Errorf("calls = %d, want 3", fs.calls())
	}
	// 429 honours Retry-After (2s); the 500 backs off 2s (attempt 2).
	if len(*slept) != 2 || (*slept)[0] != 2*time.Second || (*slept)[1] != 2*time.Second {
		t.Errorf("waits = %v", *slept)
	}
	if ct := fs.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type = %q", ct)
	}
	var m Message
	if err := json.Unmarshal([]byte(fs.bodies[0]), &m); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
}

func TestSendDoesNotRetryBadRequest(t *testing.T) {
	fs := &fakeSlack{codes: []int{http.StatusBadRequest, http.StatusOK}}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	s, _ := newTestSlack(t, srv.URL)

	err := s.Send(context.Background(), opened(SampleFinding(t0)))
	if err == nil || !strings.Contains(err.Error(), "invalid_payload") {
		t.Fatalf("want rejected error, got %v", err)
	}
	if fs.calls() != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 400)", fs.calls())
	}
}

func TestErrorsNeverContainTheURL(t *testing.T) {
	secret := "http://127.0.0.1:1/services/T000/B000/SECRETTOKEN"
	s, _ := newTestSlack(t, secret)
	err := s.Send(context.Background(), opened(SampleFinding(t0)))
	if err == nil {
		t.Fatal("want a connection error")
	}
	if strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Errorf("error leaks the webhook URL: %v", err)
	}
	if _, err := NewSlack(SlackConfig{WebhookURL: "http://hooks.slack.com/SECRETTOKEN"}, nil); err == nil || strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Errorf("want an https error without the URL, got %v", err)
	}
}

func TestSeverityFilterAndCooldown(t *testing.T) {
	s, _ := newTestSlack(t, "https://hooks.slack.com/services/T/B/x")
	ctx := context.Background()
	f := SampleFinding(t0)

	info := f
	info.Severity, info.Key = rules.Info, "info/key"
	_ = s.Notify(ctx, opened(info))
	if len(s.queue) != 0 {
		t.Fatal("info finding should be filtered (default min severity: warning)")
	}

	_ = s.Notify(ctx, opened(f))
	_ = s.Notify(ctx, rules.Event{Type: rules.EventResolved, Finding: f})
	if len(s.queue) != 2 {
		t.Fatalf("queue = %d, want opened + resolved", len(s.queue))
	}

	// The same problem comes back 3 minutes later: within the cooldown, so
	// neither its opened nor its resolved is sent.
	s.now = func() time.Time { return t0.Add(3 * time.Minute) }
	_ = s.Notify(ctx, opened(f))
	_ = s.Notify(ctx, rules.Event{Type: rules.EventResolved, Finding: f})
	if len(s.queue) != 2 {
		t.Fatalf("queue = %d, repeat within cooldown should be skipped", len(s.queue))
	}

	// After the cooldown it alerts again.
	s.now = func() time.Time { return t0.Add(15 * time.Minute) }
	_ = s.Notify(ctx, opened(f))
	if len(s.queue) != 3 {
		t.Fatalf("queue = %d, want a new alert after the cooldown", len(s.queue))
	}
}

func TestStartDelivers(t *testing.T) {
	fs := &fakeSlack{}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	s, _ := newTestSlack(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Start(ctx); close(done) }()

	if err := s.Notify(ctx, opened(SampleFinding(t0))); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for fs.calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if fs.calls() != 1 {
		t.Fatalf("calls = %d, want 1", fs.calls())
	}
	cancel()
	<-done
}

func dump(t *testing.T, m Message) string {
	t.Helper()
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // so the test sees the text as Slack does
	if err := enc.Encode(m); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

type memState struct{ marks map[string]AlertMark }

func (m *memState) AlertMarks(context.Context) (map[string]AlertMark, error) { return m.marks, nil }
func (m *memState) SaveAlertMark(_ context.Context, k string, v AlertMark) error {
	if v.LastSent.IsZero() && !v.Muted {
		delete(m.marks, k)
		return nil
	}
	m.marks[k] = v
	return nil
}

func TestCooldownSurvivesRestart(t *testing.T) {
	st := &memState{marks: map[string]AlertMark{}}
	ctx := context.Background()
	f := SampleFinding(t0)

	s, _ := newTestSlack(t, "https://hooks.slack.com/services/T/B/x")
	if err := s.UseState(ctx, st); err != nil {
		t.Fatal(err)
	}
	_ = s.Notify(ctx, opened(f))
	if !st.marks[f.Key].LastSent.Equal(t0) {
		t.Fatalf("mark not saved: %+v", st.marks)
	}

	// A new collector process, 2 minutes later: the same problem opens again.
	s2, _ := newTestSlack(t, "https://hooks.slack.com/services/T/B/x")
	s2.now = func() time.Time { return t0.Add(2 * time.Minute) }
	if err := s2.UseState(ctx, st); err != nil {
		t.Fatal(err)
	}
	_ = s2.Notify(ctx, opened(f))
	if len(s2.queue) != 0 {
		t.Fatal("restart forgot the cooldown and alerted again")
	}
	_ = s2.Notify(ctx, rules.Event{Type: rules.EventResolved, Finding: f})
	if len(s2.queue) != 0 {
		t.Fatal("resolved of a muted repeat should be skipped")
	}

	// Expired marks are ignored on load.
	s3, _ := newTestSlack(t, "https://hooks.slack.com/services/T/B/x")
	s3.now = func() time.Time { return t0.Add(time.Hour) }
	if err := s3.UseState(ctx, st); err != nil {
		t.Fatal(err)
	}
	_ = s3.Notify(ctx, opened(f))
	if len(s3.queue) != 1 {
		t.Fatal("an hour later the problem should alert again")
	}
}
