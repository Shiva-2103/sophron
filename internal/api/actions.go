package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Shiva-2103/sophron/internal/auth"
	"github.com/Shiva-2103/sophron/internal/fixes"
)

// FixReader lists fix requests (the store).
type FixReader interface {
	Fixes(ctx context.Context, status string, limit int) ([]fixes.Request, error)
}

// FixDecider approves and denies (the fixes manager).
type FixDecider interface {
	Approve(ctx context.Context, id int64, user, reason string) (fixes.Request, error)
	Deny(ctx context.Context, id int64, user, reason string) (fixes.Request, error)
}

// AuditReader lists the audit log.
type AuditReader interface {
	Audit(ctx context.Context, limit int) ([]fixes.AuditEntry, error)
}

// Check is one line of the setup page (the "doctor").
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, warn, fail, skip
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// routes adds the dashboard, login and approval endpoints.
//
//	POST /api/v1/login  {"name","password"}  starts a session (cookie)
//	POST /api/v1/logout
//	GET  /api/v1/me                          who you are, and whether login is on
//	GET  /api/v1/fixes?status=pending|all    fix requests
//	POST /api/v1/fixes/{id}/approve {"reason"}  admin only
//	POST /api/v1/fixes/{id}/deny    {"reason"}  admin only
//	GET  /api/v1/savings?window=168h         savings suggestions
//	GET  /api/v1/cost/trend?window=24h       spend and cost per 1M tokens over time
//	GET  /api/v1/audit                       the audit log, newest first
//	GET  /api/v1/setup                       setup checks ("sophron doctor")
//	GET  /                                   the dashboard
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/login", s.login)
	mux.HandleFunc("POST /api/v1/logout", func(w http.ResponseWriter, r *http.Request) {
		if s.Auth != nil {
			s.Auth.Logout(w, r)
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.FromContext(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"authEnabled": s.Auth.Enabled(), "loggedIn": ok, "user": u,
			"cluster": s.ClusterName, "canApprove": ok && u.CanApprove()})
	})
	mux.HandleFunc("GET /api/v1/fixes", func(w http.ResponseWriter, r *http.Request) {
		if s.FixReader == nil {
			writeJSON(w, http.StatusOK, []fixes.Request{})
			return
		}
		status := r.URL.Query().Get("status")
		if status == "all" {
			status = ""
		}
		list, err := s.FixReader.Fixes(r.Context(), status, 200)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if list == nil {
			list = []fixes.Request{}
		}
		writeJSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("POST /api/v1/fixes/{id}/{decision}", s.decide)
	mux.HandleFunc("GET /api/v1/savings", s.costHandler(func(r *http.Request, w time.Duration) (any, error) {
		if r.URL.Query().Get("window") == "" {
			w = 7 * 24 * time.Hour
		}
		return s.Cost.Savings(r.Context(), w)
	}))
	mux.HandleFunc("GET /api/v1/cost/trend", s.costHandler(func(r *http.Request, w time.Duration) (any, error) {
		if r.URL.Query().Get("window") == "" {
			w = 24 * time.Hour
		}
		return s.Cost.Trend(r.Context(), w)
	}))
	mux.HandleFunc("GET /api/v1/audit", func(w http.ResponseWriter, r *http.Request) {
		if s.AuditReader == nil {
			writeJSON(w, http.StatusOK, []fixes.AuditEntry{})
			return
		}
		list, err := s.AuditReader.Audit(r.Context(), 300)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if list == nil {
			list = []fixes.AuditEntry{}
		}
		writeJSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("GET /api/v1/setup", func(w http.ResponseWriter, r *http.Request) {
		checks := []Check{}
		if s.Setup != nil {
			checks = s.Setup(r.Context())
		}
		writeJSON(w, http.StatusOK, checks)
	})
	if s.UI != nil {
		files := http.FileServerFS(s.UI)
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			// One page app: unknown paths get index.html.
			if r.URL.Path != "/" {
				if _, err := fs.Stat(s.UI, strings.TrimPrefix(r.URL.Path, "/")); err != nil {
					r.URL.Path = "/"
				}
			}
			files.ServeHTTP(w, r)
		})
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.Auth.Enabled() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no admin password is set; run make admin-password (or set the sophron-admin Secret)"})
		return
	}
	var body struct{ Name, Password string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send {\"name\", \"password\"}"})
		return
	}
	u, err := s.Auth.Login(w, r, body.Name, body.Password)
	if err != nil {
		code := http.StatusUnauthorized
		if errors.Is(err, auth.ErrTooManyAttempts) {
			code = http.StatusTooManyRequests
		}
		s.audit(r.Context(), body.Name, "login.failed", "dashboard", clientAddr(r))
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	s.audit(r.Context(), u.Name, "login", "dashboard", clientAddr(r))
	writeJSON(w, http.StatusOK, map[string]any{"user": u})
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request) {
	u, ok := auth.FromContext(r.Context())
	if !ok || !u.CanApprove() {
		msg := "only an admin can approve or deny fixes"
		if !s.Auth.Enabled() {
			msg = "set an admin password first (make admin-password), then sign in"
		}
		writeJSON(w, http.StatusForbidden, map[string]string{"error": msg})
		return
	}
	if s.FixDecider == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "fixes are off"})
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad fix id"})
		return
	}
	var body struct{ Reason string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
	var req fixes.Request
	switch r.PathValue("decision") {
	case "approve":
		req, err = s.FixDecider.Approve(r.Context(), id, u.Name, body.Reason)
	case "deny":
		req, err = s.FixDecider.Deny(r.Context(), id, u.Name, body.Reason)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "use approve or deny"})
		return
	}
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, fixes.ErrNotPending) {
			code = http.StatusConflict
		}
		writeJSON(w, code, map[string]any{"error": err.Error(), "fix": req})
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// AuditWriter lets the API record logins.
type AuditWriter interface {
	AppendAudit(ctx context.Context, e fixes.AuditEntry) error
}

func (s *Server) audit(ctx context.Context, actor, event, target, detail string) {
	if s.AuditWriter == nil {
		return
	}
	_ = s.AuditWriter.AppendAudit(ctx, fixes.AuditEntry{At: time.Now().UTC(), Actor: actor, Event: event, Target: target, Detail: detail})
}

func clientAddr(r *http.Request) string {
	if i := strings.LastIndex(r.RemoteAddr, ":"); i > 0 {
		return "from " + r.RemoteAddr[:i]
	}
	return "from " + r.RemoteAddr
}

// middleware identifies the caller, guards the API when login is on, blocks
// cross-site POSTs, and sets security headers.
func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self'; connect-src 'self'; frame-ancestors 'none'")

		u, ok := s.Auth.UserFrom(r)
		if ok {
			r = r.WithContext(auth.WithUser(r.Context(), u))
		}
		api := strings.HasPrefix(r.URL.Path, "/api/")
		open := r.URL.Path == "/api/v1/login" || r.URL.Path == "/api/v1/me" || r.URL.Path == "/api/v1/logout"
		if api && s.Auth.Enabled() && !ok && !open {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in first"})
			return
		}
		// A browser only sends this custom header from our own page, so a
		// form on another site can't approve a fix (CSRF).
		if r.Method == http.MethodPost && r.Header.Get("X-Sophron") != "1" {
			if _, _, basic := r.BasicAuth(); !basic {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing X-Sophron header"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
