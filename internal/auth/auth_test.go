package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, pw string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(p, []byte(pw+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoginSessionAndBasic(t *testing.T) {
	a, err := New(Config{AdminPasswordFile: write(t, "correct-horse-1"), ViewerPasswordFile: write(t, "viewer-password-1")})
	if err != nil || !a.Enabled() {
		t.Fatalf("new: %v", err)
	}
	rec := httptest.NewRecorder()
	u, err := a.Login(rec, httptest.NewRequest("POST", "/", nil), "admin", "correct-horse-1")
	if err != nil || u.Role != RoleAdmin || !u.CanApprove() {
		t.Fatalf("login: %+v %v", u, err)
	}
	c := rec.Result().Cookies()[0]
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie not hardened: %+v", c)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(c)
	if got, ok := a.UserFrom(r); !ok || got.Name != "admin" {
		t.Errorf("session not recognised: %+v %v", got, ok)
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.SetBasicAuth("viewer", "viewer-password-1")
	if got, ok := a.UserFrom(r); !ok || got.CanApprove() {
		t.Errorf("viewer: %+v %v", got, ok)
	}
	a.Logout(httptest.NewRecorder(), func() *http.Request { r := httptest.NewRequest("GET", "/", nil); r.AddCookie(c); return r }())
	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(c)
	if _, ok := a.UserFrom(r); ok {
		t.Error("session should end at logout")
	}
}

func TestRateLimitAndRules(t *testing.T) {
	a, _ := New(Config{AdminPasswordFile: write(t, "correct-horse-1")})
	for i := 0; i < 5; i++ {
		if _, err := a.Login(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), "admin", "nope"); err == nil {
			t.Fatal("wrong password accepted")
		}
	}
	if _, err := a.Login(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), "admin", "correct-horse-1"); !errors.Is(err, ErrTooManyAttempts) {
		t.Errorf("want rate limit after 5 failures, got %v", err)
	}
	if _, err := New(Config{AdminPasswordFile: write(t, "short")}); err == nil {
		t.Error("a short password should be refused")
	}
	if off, _ := New(Config{AdminPasswordFile: filepath.Join(t.TempDir(), "missing")}); off.Enabled() {
		t.Error("no password file: login off")
	}
}
