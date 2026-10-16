package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/webconf"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	app := core.New()
	app.Paths.StateDir = t.TempDir()
	s, err := app.Init()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	hash, err := webconf.HashPassword("very secret password")
	if err != nil {
		t.Fatal(err)
	}
	s.SetSetting(webconf.KeyPassword, hash)
	srv := httptest.NewServer(New(app, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, c *http.Client, url, csrf, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestAuthFlow(t *testing.T) {
	srv := testServer(t)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}

	resp, err := c.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Error("CSP header missing")
	}
	resp, _ = c.Get(srv.URL + "/api/journal")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("journal without login: %d", resp.StatusCode)
	}
	if r := post(t, c, srv.URL+"/api/login", "", `{"password":"wrong password!"}`); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password: %d", r.StatusCode)
	}
	if r := post(t, c, srv.URL+"/api/login", "", `{"password":"very secret password"}`); r.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", r.StatusCode)
	}
	resp, _ = c.Get(srv.URL + "/api/journal")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("journal after login: %d", resp.StatusCode)
	}
	if r := post(t, c, srv.URL+"/api/logout", "", ``); r.StatusCode != http.StatusForbidden {
		t.Errorf("POST without CSRF token: %d", r.StatusCode)
	}
	resp, _ = c.Get(srv.URL + "/metrics")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("metrics must be off by default: %d", resp.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	srv := testServer(t)
	c := &http.Client{}
	var last int
	for range maxFailures + 1 {
		last = post(t, c, srv.URL+"/api/login", "", `{"password":"nope nope nope"}`).StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("expected 429 after %d failures, got %d", maxFailures, last)
	}
}

func TestForeignHostRejected(t *testing.T) {
	srv := testServer(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/session", nil)
	req.Host = "rebind.example.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("foreign Host accepted: %d", resp.StatusCode)
	}
}

func TestTOTPReplay(t *testing.T) {
	l := newLimiter()
	secret := NewTOTPSecret()
	now := time.Now()
	step, ok := totpStep(secret, TOTPCode(secret, now), now)
	if !ok || !l.useStep(step) {
		t.Fatal("fresh code rejected")
	}
	if l.useStep(step) {
		t.Error("replayed code accepted")
	}
	if _, ok := totpStep("", "000000", now); ok {
		t.Error("empty secret accepted")
	}
}

func TestForwardedHeadersIgnoredWithoutProxy(t *testing.T) {
	srv := testServer(t)
	var last int
	for i := range maxFailures + 1 {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/login", strings.NewReader(`{"password":"nope nope nope"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", "203.0.113."+string(rune('1'+i)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		last = resp.StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("X-Forwarded-For bypassed the limiter: %d", last)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/session", nil)
	req.Host = "rebind.example.com"
	req.Header.Set("X-Forwarded-Host", "127.0.0.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("X-Forwarded-Host accepted without a proxy: %d", resp.StatusCode)
	}
}

func TestSecureCookieOnlyOverHTTPS(t *testing.T) {
	s := &Server{exposed: true}
	r := httptest.NewRequest(http.MethodPost, "http://100.64.0.1:8080/api/login", nil)
	if s.secure(r) {
		t.Error("plain HTTP on an exposed address must not set Secure")
	}
	r.Header.Set("X-Forwarded-Proto", "https")
	if !s.secure(r) {
		t.Error("HTTPS behind a proxy must set Secure")
	}
}
