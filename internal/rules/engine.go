package rules

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Shiva-2103/sophron/internal/collector"
)

// Store is what the engine needs from the database.
type Store interface {
	Since(ctx context.Context, since time.Time) ([]collector.Snapshot, error)
	OpenFindings(ctx context.Context) ([]Finding, error)
	InsertFinding(ctx context.Context, f Finding) (int64, error)
	UpdateFinding(ctx context.Context, f Finding) error
	ResolveFinding(ctx context.Context, id int64, at time.Time, r *Resolution) error
}

// Event types sent to notifiers.
const (
	EventOpened   = "opened"
	EventResolved = "resolved"
	EventEscalate = "escalated" // severity went up
)

// Event is something a person may want to hear about.
type Event struct {
	Type    string  `json:"type"`
	Finding Finding `json:"finding"`
}

// Notifier delivers events (logs today; Slack and webhooks next).
type Notifier interface {
	Notify(ctx context.Context, e Event) error
}

// Engine runs the detectors after each collection cycle and keeps the
// findings table in step with reality.
type Engine struct {
	Store     Store
	Detectors []Detector
	Config    Config
	Notifiers []Notifier
	// Optional: finds which tool last changed a workload, for resolutions.
	Changes ChangeFinder
	Log     *slog.Logger
	// A finding is resolved once it hasn't been seen for this long.
	// Waiting a little avoids open/resolve/open flapping.
	ResolveAfter time.Duration
	Now          func() time.Time

	mu   sync.Mutex
	last Status
}

// Status describes the last engine run, for the API.
type Status struct {
	LastRun  time.Time `json:"lastRun"`
	Open     int       `json:"open"`
	Opened   int       `json:"opened"`
	Updated  int       `json:"updated"`
	Resolved int       `json:"resolved"`
	Error    string    `json:"error,omitempty"`
}

// Status returns the last run's result.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.last
}

// Process runs once. Call it after each collection cycle, with the snapshots
// that cycle saved (possibly none).
func (e *Engine) Process(ctx context.Context, latest []collector.Snapshot) (Status, error) {
	st, err := e.process(ctx, latest)
	if err != nil {
		st.Error = err.Error()
	}
	e.mu.Lock()
	e.last = st
	e.mu.Unlock()
	return st, err
}

func (e *Engine) process(ctx context.Context, latest []collector.Snapshot) (Status, error) {
	now := e.now()
	st := Status{LastRun: now}

	hist, err := e.Store.Since(ctx, now.Add(-e.Config.HistoryWindow()))
	if err != nil {
		return st, fmt.Errorf("load history: %w", err)
	}
	in := Input{Now: now, Latest: latest, History: map[PodKey][]collector.Snapshot{}, Config: e.Config}
	for _, h := range hist {
		k := PodKey{h.Namespace, h.Pod}
		in.History[k] = append(in.History[k], h)
	}

	// Run every detector. One finding per key, keeping the most severe.
	found := map[string]Finding{}
	for _, d := range e.Detectors {
		for _, f := range e.safeDetect(d, in) {
			f.Detector = d.Name()
			f.Key = f.MakeKey()
			if cur, ok := found[f.Key]; !ok || f.Severity.Rank() > cur.Severity.Rank() {
				found[f.Key] = f
			}
		}
	}

	open, err := e.Store.OpenFindings(ctx)
	if err != nil {
		return st, fmt.Errorf("load open findings: %w", err)
	}
	byKey := map[string]Finding{}
	for _, f := range open {
		byKey[f.Key] = f
	}

	var errs []error
	for key, f := range found {
		f.LastSeen = now
		if cur, ok := byKey[key]; ok {
			f.ID, f.FirstSeen, f.Status = cur.ID, cur.FirstSeen, StatusOpen
			if err := e.Store.UpdateFinding(ctx, f); err != nil {
				errs = append(errs, err)
				continue
			}
			st.Updated++
			if f.Severity.Rank() > cur.Severity.Rank() {
				e.notify(ctx, Event{Type: EventEscalate, Finding: f})
			}
			continue
		}
		f.FirstSeen, f.Status = now, StatusOpen
		id, err := e.Store.InsertFinding(ctx, f)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		f.ID = id
		st.Opened++
		e.notify(ctx, Event{Type: EventOpened, Finding: f})
	}

	for key, f := range byKey {
		if _, still := found[key]; still {
			continue
		}
		if now.Sub(f.LastSeen) < e.resolveAfter() {
			continue
		}
		res := e.resolution(ctx, f, in)
		if err := e.Store.ResolveFinding(ctx, f.ID, now, &res); err != nil {
			errs = append(errs, err)
			continue
		}
		f.Status, f.ResolvedAt, f.Resolution = StatusResolved, now, &res
		st.Resolved++
		e.notify(ctx, Event{Type: EventResolved, Finding: f})
	}

	st.Open = len(byKey) + st.Opened - st.Resolved
	return st, errors.Join(errs...)
}

// resolution explains how a finding went away. Looking up who changed the
// workload is best effort: a failure only leaves ChangedBy empty.
func (e *Engine) resolution(ctx context.Context, f Finding, in Input) Resolution {
	r := explainResolution(f, in)
	r.DownSeconds, r.DownCost = downtime(f, e.findingSnapshots(ctx, f, in))
	if e.Changes == nil || f.WorkloadName == "" {
		return r
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	manager, at, err := e.Changes.LastChange(ctx, f.WorkloadKind, f.Namespace, f.WorkloadName)
	if err != nil {
		e.Log.Debug("could not look up who changed the workload", "target", f.Target(), "error", err)
		return r
	}
	// Only a change made while the problem was open can be the fix.
	if manager != "" && !at.Before(f.FirstSeen.Add(-time.Second)) {
		r.ChangedBy, r.ChangedAt = FriendlyManager(manager), at
	}
	return r
}

// findingSnapshots returns snapshots covering the finding's whole life:
// the history already loaded when it fits, otherwise a fresh query.
func (e *Engine) findingSnapshots(ctx context.Context, f Finding, in Input) []collector.Snapshot {
	if !f.FirstSeen.Before(in.Now.Add(-in.Config.HistoryWindow())) {
		var out []collector.Snapshot
		for _, h := range in.History {
			out = append(out, h...)
		}
		return out
	}
	snaps, err := e.Store.Since(ctx, f.FirstSeen)
	if err != nil {
		e.Log.Debug("could not load snapshots for downtime", "finding", f.Key, "error", err)
		return nil
	}
	return snaps
}

// safeDetect stops one buggy detector from taking the whole engine down.
func (e *Engine) safeDetect(d Detector, in Input) (out []Finding) {
	defer func() {
		if r := recover(); r != nil {
			e.Log.Error("detector panicked", "detector", d.Name(), "panic", fmt.Sprint(r))
			out = nil
		}
	}()
	return d.Detect(in)
}

func (e *Engine) notify(ctx context.Context, ev Event) {
	for _, n := range e.Notifiers {
		if err := n.Notify(ctx, ev); err != nil {
			e.Log.Warn("notify failed", "event", ev.Type, "finding", ev.Finding.Key, "error", err)
		}
	}
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC()
}

func (e *Engine) resolveAfter() time.Duration {
	if e.ResolveAfter > 0 {
		return e.ResolveAfter
	}
	return 90 * time.Second
}

// LogNotifier writes events to the log, one structured line each.
type LogNotifier struct{ Log *slog.Logger }

// Notify logs the event.
func (n LogNotifier) Notify(_ context.Context, e Event) error {
	f := e.Finding
	n.Log.Info("finding "+e.Type, "detector", f.Detector, "severity", string(f.Severity),
		"target", f.Target(), "title", f.Title)
	return nil
}
