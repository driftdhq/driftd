package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

type contextKey string

const (
	csrfContextKey contextKey = "csrf"
	csrfCookieName            = "driftd_csrf"
)

func (s *Server) uiAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != s.cfg.UIAuth.Username || password != s.cfg.UIAuth.Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="driftd"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) apiAuthEnabled() bool {
	return s.cfg.APIAuth.Token != "" || s.cfg.APIAuth.Username != "" || s.cfg.APIAuth.Password != ""
}

func (s *Server) apiAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Webhook.Enabled && strings.HasPrefix(r.URL.Path, "/api/webhooks/") {
			next.ServeHTTP(w, r)
			return
		}

		if s.cfg.APIAuth.Token != "" {
			token := r.Header.Get(s.cfg.APIAuth.TokenHeader)
			if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.APIAuth.Token)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}

		if s.cfg.APIAuth.Username != "" || s.cfg.APIAuth.Password != "" {
			username, password, ok := r.BasicAuth()
			if ok &&
				subtle.ConstantTimeCompare([]byte(username), []byte(s.cfg.APIAuth.Username)) == 1 &&
				subtle.ConstantTimeCompare([]byte(password), []byte(s.cfg.APIAuth.Password)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}

		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}

func (s *Server) settingsAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.apiAuthEnabled() {
			s.apiAuthMiddleware(next).ServeHTTP(w, r)
			return
		}

		if s.cfg.UIAuth.Username != "" || s.cfg.UIAuth.Password != "" {
			username, password, ok := r.BasicAuth()
			if ok &&
				subtle.ConstantTimeCompare([]byte(username), []byte(s.cfg.UIAuth.Username)) == 1 &&
				subtle.ConstantTimeCompare([]byte(password), []byte(s.cfg.UIAuth.Password)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="driftd"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := s.ensureCSRFToken(w, r)
		ctx := context.WithValue(r.Context(), csrfContextKey, token)

		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "Invalid form", http.StatusBadRequest)
				return
			}
			formToken := r.FormValue("csrf_token")
			if formToken == "" || subtle.ConstantTimeCompare([]byte(formToken), []byte(token)) != 1 {
				http.Error(w, "Invalid CSRF token", http.StatusBadRequest)
				return
			}
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		limiter := s.getRateLimiter(ip)
		if !limiter.Allow() {
			http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ensureCSRFToken(w http.ResponseWriter, r *http.Request) string {
	if cookie, err := r.Cookie(csrfCookieName); err == nil && cookie.Value != "" {
		return cookie.Value
	}

	token := generateToken(32)
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return token
}

func csrfTokenFromContext(ctx context.Context) string {
	if token, ok := ctx.Value(csrfContextKey).(string); ok {
		return token
	}
	return ""
}

func generateToken(length int) string {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func clientIP(r *http.Request) string {
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded != "" {
		parts := strings.Split(forwarded, ",")
		return strings.TrimSpace(parts[0])
	}

	realIP := r.Header.Get("X-Real-IP")
	if realIP != "" {
		return realIP
	}

	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

func (s *Server) getRateLimiter(ip string) *rate.Limiter {
	s.rateLimitMu.Lock()
	defer s.rateLimitMu.Unlock()

	if entry, ok := s.rateLimiters[ip]; ok {
		entry.lastSeen = time.Now()
		return entry.limiter
	}

	limit := rate.Limit(1)
	burst := 5
	if s.cfg.API.RateLimitPerMinute > 0 {
		limit = rate.Limit(float64(s.cfg.API.RateLimitPerMinute) / 60.0)
		burst = s.cfg.API.RateLimitPerMinute
	}
	limiter := rate.NewLimiter(limit, burst)
	s.rateLimiters[ip] = &rateLimiterEntry{limiter: limiter, lastSeen: time.Now()}

	if len(s.rateLimiters) > 1000 {
		cutoff := time.Now().Add(-5 * time.Minute)
		for key, entry := range s.rateLimiters {
			if entry.lastSeen.Before(cutoff) {
				delete(s.rateLimiters, key)
			}
		}
	}

	return limiter
}
