// Package api serves the collector's health checks and stored snapshots
// over HTTP. The dashboard (Phase 5) will use the same endpoints.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/Shiva-2103/sophron/internal/collector"
	"github.com/Shiva-2103/sophron/internal/cost"
	"github.com/Shiva-2103/sophron/internal/rules"
)

// CostReader is what the cost endpoints need. cost.Reporter implements it.
type CostReader interface {
	Models(ctx context.Context, window time.Duration) ([]cost.ModelCost, error)
	Nodes(ctx context.Context) ([]cost.NodeCost, error)
	Summary(ctx context.Context, window time.Duration) (cost.Summary, error)
}

// FindingsReader is the part of the store the findings API needs.
type FindingsReader interface {
	Findings(ctx context.Context, status string, limit int) ([]rules.Finding, error)
}

// Reader is the part of the store the API needs.
type Reader interface {
	Latest(ctx context.Context, maxAge time.Duration) ([]collector.Snapshot, error)
	History(ctx context.Context, namespace, pod string, since time.Time) ([]collector.Snapshot, error)
	Ping(ctx context.Context) error
}

// Server holds the API's dependencies.
type Server struct {
	Store    Reader
	Status   func() collector.Status
	Synced   func() bool
	Interval time.Duration

	// Rules engine (optional: nil when --rules=false).
	Findings  FindingsReader
	Rules     func() rules.Status
	Detectors []rules.Detector

	// Cost engine (optional).
	Cost CostReader
}

// Handler returns the HTTP routes.
//
//	GET /healthz                       the process is alive
//	GET /readyz                        informers synced and a recent cycle succeeded
//	GET /api/v1/status                 details of the last cycle
//	GET /api/v1/snapshots/latest       newest snapshot per pod (last 10 minutes)
//	GET /api/v1/snapshots?namespace=&pod=&since=15m   one pod's history
//	GET /api/v1/findings?status=open|resolved|all     problems Sophron found
//	GET /api/v1/rules/status                          last rules engine run
//	GET /api/v1/detectors                             what each detector looks for
//	GET /api/v1/cost/summary?window=1h                spend, idle, cost per 1M tokens
//	GET /api/v1/cost/models?window=1h                 cost and throughput per model
//	GET /api/v1/cost/nodes                            each node: price, model, other, idle
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.Status())
	})
	mux.HandleFunc("GET /api/v1/snapshots/latest", s.latest)
	mux.HandleFunc("GET /api/v1/snapshots", s.history)
	mux.HandleFunc("GET /api/v1/findings", s.findings)
	mux.HandleFunc("GET /api/v1/rules/status", func(w http.ResponseWriter, _ *http.Request) {
		if s.Rules == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "rules engine is off (--rules=false)"})
			return
		}
		writeJSON(w, http.StatusOK, s.Rules())
	})
	mux.HandleFunc("GET /api/v1/cost/summary", s.costHandler(func(r *http.Request, w time.Duration) (any, error) {
		return s.Cost.Summary(r.Context(), w)
	}))
	mux.HandleFunc("GET /api/v1/cost/models", s.costHandler(func(r *http.Request, w time.Duration) (any, error) {
		m, err := s.Cost.Models(r.Context(), w)
		if m == nil {
			m = []cost.ModelCost{}
		}
		return m, err
	}))
	mux.HandleFunc("GET /api/v1/cost/nodes", s.costHandler(func(r *http.Request, _ time.Duration) (any, error) {
		n, err := s.Cost.Nodes(r.Context())
		if n == nil {
			n = []cost.NodeCost{}
		}
		return n, err
	}))
	mux.HandleFunc("GET /api/v1/detectors", func(w http.ResponseWriter, _ *http.Request) {
		type d struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		out := []d{}
		for _, x := range s.Detectors {
			out = append(out, d{x.Name(), x.Description()})
		}
		writeJSON(w, http.StatusOK, out)
	})
	return mux
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.Synced != nil && !s.Synced() {
		http.Error(w, "informers not synced yet", http.StatusServiceUnavailable)
		return
	}
	if err := s.Store.Ping(r.Context()); err != nil {
		http.Error(w, "database: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	st := s.Status()
	if st.LastRun.IsZero() {
		http.Error(w, "first collection cycle not finished", http.StatusServiceUnavailable)
		return
	}
	if st.Error != "" {
		http.Error(w, "last cycle failed: "+st.Error, http.StatusServiceUnavailable)
		return
	}
	if time.Since(st.LastRun) > 3*s.Interval {
		http.Error(w, "no collection cycle in "+(3*s.Interval).String(), http.StatusServiceUnavailable)
		return
	}
	_, _ = w.Write([]byte("ready\n"))
}

func (s *Server) latest(w http.ResponseWriter, r *http.Request) {
	snaps, err := s.Store.Latest(r.Context(), 10*time.Minute)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if snaps == nil {
		snaps = []collector.Snapshot{}
	}
	writeJSON(w, http.StatusOK, snaps)
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	ns, pod := r.URL.Query().Get("namespace"), r.URL.Query().Get("pod")
	if ns == "" || pod == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "namespace and pod are required"})
		return
	}
	window := 15 * time.Minute
	if v := r.URL.Query().Get("since"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 || d > 7*24*time.Hour {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "since must be a duration like 15m or 2h, up to 168h"})
			return
		}
		window = d
	}
	snaps, err := s.Store.History(r.Context(), ns, pod, time.Now().Add(-window))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if snaps == nil {
		snaps = []collector.Snapshot{}
	}
	writeJSON(w, http.StatusOK, snaps)
}

func (s *Server) findings(w http.ResponseWriter, r *http.Request) {
	if s.Findings == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "rules engine is off (--rules=false)"})
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "open":
		status = rules.StatusOpen
	case "all":
		status = ""
	case rules.StatusResolved:
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status must be open, resolved or all"})
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be 1 to 1000"})
			return
		}
		limit = n
	}
	fs, err := s.Findings.Findings(r.Context(), status, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if fs == nil {
		fs = []rules.Finding{}
	}
	writeJSON(w, http.StatusOK, fs)
}

// costHandler parses ?window= (default 1h, up to 7 days) and runs fn.
func (s *Server) costHandler(fn func(r *http.Request, window time.Duration) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Cost == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "cost engine is off (--cost=false)"})
			return
		}
		window := time.Hour
		if v := r.URL.Query().Get("window"); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d < 5*time.Minute || d > 7*24*time.Hour {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "window must be a duration from 5m to 168h, like 1h or 24h"})
				return
			}
			window = d
		}
		v, err := fn(r, window)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
