package rules

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Shiva-2103/sophron/internal/collector"
)

type memStore struct {
	hist     []collector.Snapshot
	findings map[int64]*Finding
	nextID   int64
}

func (m *memStore) Since(context.Context, time.Time) ([]collector.Snapshot, error) {
	return m.hist, nil
}
func (m *memStore) OpenFindings(context.Context) ([]Finding, error) {
	var out []Finding
	for _, f := range m.findings {
		if f.Status == StatusOpen {
			out = append(out, *f)
		}
	}
	return out, nil
}
func (m *memStore) InsertFinding(_ context.Context, f Finding) (int64, error) {
	m.nextID++
	f.ID = m.nextID
	m.findings[f.ID] = &f
	return f.ID, nil
}
func (m *memStore) UpdateFinding(_ context.Context, f Finding) error {
	m.findings[f.ID] = &f
	return nil
}
func (m *memStore) ResolveFinding(_ context.Context, id int64, at time.Time) error {
	m.findings[id].Status, m.findings[id].ResolvedAt = StatusResolved, at
	return nil
}

type recorder struct{ events []Event }

func (r *recorder) Notify(_ context.Context, e Event) error {
	r.events = append(r.events, e)
	return nil
}

// fixedDetector reports whatever findings the test sets.
type fixedDetector struct{ out *[]Finding }

func (d fixedDetector) Name() string           { return "test" }
func (d fixedDetector) Description() string    { return "test" }
func (d fixedDetector) Detect(Input) []Finding { return *d.out }

type panicDetector struct{}

func (panicDetector) Name() string           { return "boom" }
func (panicDetector) Description() string    { return "boom" }
func (panicDetector) Detect(Input) []Finding { panic("bug") }

func TestEngineLifecycle(t *testing.T) {
	clock := now
	var report []Finding
	store := &memStore{findings: map[int64]*Finding{}}
	rec := &recorder{}
	e := &Engine{
		Store: store, Config: DefaultConfig(), Notifiers: []Notifier{rec},
		Detectors:    []Detector{panicDetector{}, fixedDetector{&report}},
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		ResolveAfter: time.Minute, Now: func() time.Time { return clock },
	}
	problem := Finding{Namespace: "llm", WorkloadName: "llm-server", Severity: Warning, Title: "x"}
	step := func(d time.Duration) Status {
		t.Helper()
		clock = clock.Add(d)
		st, err := e.Process(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}

	report = []Finding{problem}
	if st := step(0); st.Opened != 1 || st.Open != 1 {
		t.Fatalf("cycle 1: %+v", st)
	}
	if len(rec.events) != 1 || rec.events[0].Type != EventOpened || rec.events[0].Finding.Key != "test/llm/llm-server" {
		t.Fatalf("want one opened event, got %+v", rec.events)
	}

	// Still there: updated, no new notification.
	if st := step(30 * time.Second); st.Updated != 1 || st.Opened != 0 || len(rec.events) != 1 {
		t.Fatalf("cycle 2: %+v, events %d", st, len(rec.events))
	}

	// Gets worse: escalation event.
	report[0].Severity = Critical
	step(30 * time.Second)
	if len(rec.events) != 2 || rec.events[1].Type != EventEscalate {
		t.Fatalf("want escalation, got %+v", rec.events)
	}

	// Gone, but only 30s: stays open (no flapping).
	report = nil
	if st := step(30 * time.Second); st.Resolved != 0 || st.Open != 1 {
		t.Fatalf("cycle 4: %+v", st)
	}
	// Gone for over a minute: resolved.
	if st := step(45 * time.Second); st.Resolved != 1 || st.Open != 0 {
		t.Fatalf("cycle 5: %+v", st)
	}
	if rec.events[len(rec.events)-1].Type != EventResolved {
		t.Fatalf("want resolved event, got %+v", rec.events)
	}

	// Comes back: a new finding, not the old one reopened.
	report = []Finding{problem}
	step(30 * time.Second)
	if len(store.findings) != 2 {
		t.Fatalf("want a second finding row, got %d", len(store.findings))
	}
}
