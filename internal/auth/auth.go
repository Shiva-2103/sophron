// Package auth handles dashboard logins: a local admin (and optional viewer)
// whose passwords live in Kubernetes Secrets, server-side sessions in an
// HttpOnly cookie, and basic auth for scripts.
//
// No password set means nobody can log in as admin: the dashboard stays
// read-only and nothing can be approved. That is the safe default.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Roles.
const (
	RoleAdmin  = "admin"  // approve fixes, change settings
	RoleViewer = "viewer" // read only
)

// CookieName is the session cookie.
const CookieName = "sophron_session"

// User is who is calling.
type User struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// CanApprove reports whether the user may approve or deny fixes.
func (u User) CanApprove() bool { return u.Role == RoleAdmin }

type account struct {
	name, role, password string
}

type session struct {
	user    User
	expires time.Time
}

// Auth checks passwords and keeps sessions.
type Auth struct {
	accounts []account
	ttl      time.Duration

	mu       sync.Mutex
	sessions map[string]session
	failures map[string][]time.Time // per client IP, for rate limiting
}

// Config says where the passwords are.
type Config struct {
	AdminPasswordFile  string // e.g. /etc/sophron/admin/password
	ViewerPasswordFile string // optional
	SessionTTL         time.Duration
}

// New reads the password files. Missing files are fine: that account is off.
func New(c Config) (*Auth, error) {
	a := &Auth{ttl: c.SessionTTL, sessions: map[string]session{}, failures: map[string][]time.Time{}}
	if a.ttl == 0 {
		a.ttl = 12 * time.Hour
	}
	for _, x := range []struct{ name, role, file string }{
		{"admin", RoleAdmin, c.AdminPasswordFile},
		{"viewer", RoleViewer, c.ViewerPasswordFile},
	} {
		if x.file == "" {
			continue
		}
		b, err := os.ReadFile(x.file) //nolint:gosec // path comes from the operator's flag
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if pw := strings.TrimSpace(string(b)); pw != "" {
			if len(pw) < 12 {
				return nil, errors.New("the " + x.name + " password must be at least 12 characters")
			}
			a.accounts = append(a.accounts, account{x.name, x.role, pw})
		}
	}
	return a, nil
}

// Enabled reports whether any account can log in.
func (a *Auth) Enabled() bool { return a != nil && len(a.accounts) > 0 }

// Check verifies a name and password, in constant time.
func (a *Auth) Check(name, password string) (User, bool) {
	var found *account
	for i := range a.accounts {
		ok := subtle.ConstantTimeCompare([]byte(a.accounts[i].name), []byte(name)) == 1
		ok = subtle.ConstantTimeCompare([]byte(a.accounts[i].password), []byte(password)) == 1 && ok
		if ok {
			found = &a.accounts[i]
		}
	}
	if found == nil {
		return User{}, false
	}
	return User{Name: found.name, Role: found.role}, true
}

// ErrTooManyAttempts slows down password guessing.
var ErrTooManyAttempts = errors.New("too many failed logins; wait a minute")

// Login checks the password and starts a session.
func (a *Auth) Login(w http.ResponseWriter, r *http.Request, name, password string) (User, error) {
	ip := clientIP(r)
	a.mu.Lock()
	recent := a.failures[ip][:0]
	for _, t := range a.failures[ip] {
		if time.Since(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	a.failures[ip] = recent
	blocked := len(recent) >= 5
	a.mu.Unlock()
	if blocked {
		return User{}, ErrTooManyAttempts
	}
	u, ok := a.Check(name, password)
	if !ok {
		a.mu.Lock()
		a.failures[ip] = append(a.failures[ip], time.Now())
		a.mu.Unlock()
		return User{}, errors.New("wrong name or password")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return User{}, err
	}
	token := hex.EncodeToString(b)
	a.mu.Lock()
	a.sessions[token] = session{user: u, expires: time.Now().Add(a.ttl)}
	a.mu.Unlock()
	// Secure always: browsers treat http://localhost (port-forward, SSM
	// tunnel) as secure, and anything else should be behind HTTPS anyway.
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(a.ttl.Seconds())})
	return u, nil
}

// Logout ends the session.
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

// UserFrom finds the caller: a session cookie, or basic auth (for scripts).
func (a *Auth) UserFrom(r *http.Request) (User, bool) {
	if !a.Enabled() {
		return User{}, false
	}
	if c, err := r.Cookie(CookieName); err == nil {
		a.mu.Lock()
		s, ok := a.sessions[c.Value]
		if ok && time.Now().After(s.expires) {
			delete(a.sessions, c.Value)
			ok = false
		}
		a.mu.Unlock()
		if ok {
			return s.user, true
		}
	}
	if name, pw, ok := r.BasicAuth(); ok {
		return a.Check(name, pw)
	}
	return User{}, false
}

type ctxKey struct{}

// WithUser stores the caller in the request context.
func WithUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// FromContext returns the caller set by the middleware.
func FromContext(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(ctxKey{}).(User)
	return u, ok
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
