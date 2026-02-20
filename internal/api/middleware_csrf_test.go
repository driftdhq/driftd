package api

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/driftdhq/driftd/internal/config"
)

func TestEnsureCSRFTokenSetsSecureCookie(t *testing.T) {
	srv := &Server{cfg: &config.Config{}}
	req := httptest.NewRequest(http.MethodGet, "http://example.com", nil)
	rec := httptest.NewRecorder()

	token := srv.ensureCSRFToken(rec, req)
	if token == "" {
		t.Fatal("expected non-empty csrf token")
	}

	resp := rec.Result()
	defer resp.Body.Close()

	var csrf *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == csrfCookieName {
			csrf = c
			break
		}
	}
	if csrf == nil {
		t.Fatalf("expected %q cookie to be set", csrfCookieName)
	}
	if !csrf.Secure {
		t.Fatal("expected csrf cookie to always be secure")
	}
}

func TestShouldBypassCSRFCheck(t *testing.T) {
	tests := []struct {
		name         string
		insecureDev  bool
		forwarded    string
		withTLS      bool
		remoteAddr   string
		expectBypass bool
	}{
		{
			name:         "bypass on insecure dev over plain http",
			insecureDev:  true,
			remoteAddr:   "127.0.0.1:1234",
			expectBypass: true,
		},
		{
			name:         "do not bypass on insecure dev with forwarded https",
			insecureDev:  true,
			forwarded:    "https",
			remoteAddr:   "127.0.0.1:1234",
			expectBypass: false,
		},
		{
			name:         "do not bypass on insecure dev with tls",
			insecureDev:  true,
			withTLS:      true,
			remoteAddr:   "127.0.0.1:1234",
			expectBypass: false,
		},
		{
			name:         "do not bypass on insecure dev for non-localhost request",
			insecureDev:  true,
			remoteAddr:   "203.0.113.10:4321",
			expectBypass: false,
		},
		{
			name:         "bypass on insecure dev for ipv6 loopback",
			insecureDev:  true,
			remoteAddr:   "[::1]:1234",
			expectBypass: true,
		},
		{
			name:         "do not bypass outside insecure dev mode",
			insecureDev:  false,
			remoteAddr:   "127.0.0.1:1234",
			expectBypass: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://example.com", nil)
			if tt.forwarded != "" {
				req.Header.Set("X-Forwarded-Proto", tt.forwarded)
			}
			if tt.remoteAddr != "" {
				req.RemoteAddr = tt.remoteAddr
			}
			if tt.withTLS {
				req.TLS = &tls.ConnectionState{}
			}
			srv := &Server{cfg: &config.Config{InsecureDevMode: tt.insecureDev}}
			if got := srv.shouldBypassCSRFCheck(req); got != tt.expectBypass {
				t.Fatalf("shouldBypassCSRFCheck() = %v, want %v", got, tt.expectBypass)
			}
		})
	}
}

func TestCSRFMiddlewareBypassesTokenCheckForInsecureDevHTTP(t *testing.T) {
	srv := &Server{cfg: &config.Config{InsecureDevMode: true}}
	h := srv.csrfMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "http://example.com", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "127.0.0.1:9999"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected middleware to bypass csrf check on insecure dev http, got %d", rec.Code)
	}
}

func TestCSRFMiddlewareEnforcesTokenCheckOutsideBypass(t *testing.T) {
	srv := &Server{cfg: &config.Config{InsecureDevMode: false}}
	h := srv.csrfMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "http://example.com", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected middleware to reject missing csrf token, got %d", rec.Code)
	}
}

func TestCSRFMiddlewareDoesNotBypassForNonLocalRequest(t *testing.T) {
	srv := &Server{cfg: &config.Config{InsecureDevMode: true}}
	h := srv.csrfMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "http://example.com", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "198.51.100.8:7777"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected csrf rejection for non-local request, got %d", rec.Code)
	}
}
