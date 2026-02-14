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

type authRole int

const (
	roleNone authRole = iota
	roleViewer
	roleOperator
	roleAdmin
)

func (s *Server) uiAuthMiddleware(next http.Handler) http.Handler {
	if s.useExternalAuth() {
		return s.externalRoleMiddleware(roleViewer)(next)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(username), []byte(s.cfg.UIAuth.Username)) != 1 ||
			subtle.ConstantTimeCompare([]byte(password), []byte(s.cfg.UIAuth.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="driftd"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) uiWriteAuthMiddleware(next http.Handler) http.Handler {
	if !s.useExternalAuth() {
		return next
	}
	return s.externalRoleMiddleware(roleOperator)(next)
}

func (s *Server) uiSettingsAuthMiddleware(next http.Handler) http.Handler {
	if !s.useExternalAuth() {
		return next
	}
	return s.externalRoleMiddleware(roleAdmin)(next)
}

func (s *Server) useExternalAuth() bool {
	if s == nil || s.cfg == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(s.cfg.Auth.Mode), "external")
}

func (s *Server) parseExternalDefaultRole() authRole {
	switch strings.ToLower(strings.TrimSpace(s.cfg.Auth.External.DefaultRole)) {
	case "none":
		return roleNone
	case "operator":
		return roleOperator
	case "admin":
		return roleAdmin
	case "viewer":
		fallthrough
	default:
		return roleViewer
	}
}

func (s *Server) parseExternalGroups(raw string) map[string]struct{} {
	groups := map[string]struct{}{}
	delimiter := s.cfg.Auth.External.GroupsDelimiter
	if delimiter == "" {
		delimiter = ","
	}
	for _, part := range strings.Split(raw, delimiter) {
		group := strings.ToLower(strings.TrimSpace(part))
		if group == "" {
			continue
		}
		groups[group] = struct{}{}
	}
	return groups
}

func roleMatchesAny(groups map[string]struct{}, candidates []string) bool {
	for _, candidate := range candidates {
		normalized := strings.ToLower(strings.TrimSpace(candidate))
		if normalized == "" {
			continue
		}
		if normalized == "*" {
			return true
		}
		if _, ok := groups[normalized]; ok {
			return true
		}
	}
	return false
}

func maxRole(a, b authRole) authRole {
	if b > a {
		return b
	}
	return a
}

func (s *Server) externalRoleFromRequest(r *http.Request) (authRole, bool) {
	userHeader := strings.TrimSpace(s.cfg.Auth.External.UserHeader)
	if userHeader == "" {
		userHeader = "X-Auth-Request-User"
	}
	emailHeader := strings.TrimSpace(s.cfg.Auth.External.EmailHeader)
	if emailHeader == "" {
		emailHeader = "X-Auth-Request-Email"
	}
	groupsHeader := strings.TrimSpace(s.cfg.Auth.External.GroupsHeader)
	if groupsHeader == "" {
		groupsHeader = "X-Auth-Request-Groups"
	}

	subject := strings.TrimSpace(r.Header.Get(userHeader))
	if subject == "" {
		subject = strings.TrimSpace(r.Header.Get(emailHeader))
	}
	if subject == "" {
		return roleNone, false
	}

	role := s.parseExternalDefaultRole()
	groups := s.parseExternalGroups(r.Header.Get(groupsHeader))
	if roleMatchesAny(groups, s.cfg.Auth.External.Roles.Viewers) {
		role = maxRole(role, roleViewer)
	}
	if roleMatchesAny(groups, s.cfg.Auth.External.Roles.Operators) {
		role = maxRole(role, roleOperator)
	}
	if roleMatchesAny(groups, s.cfg.Auth.External.Roles.Admins) {
		role = maxRole(role, roleAdmin)
	}

	return role, true
}

func (s *Server) externalRoleMiddleware(required authRole) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role, ok := s.externalRoleFromRequest(r)
			if !ok {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			if role < required {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) apiAuthEnabled() bool {
	if s.useExternalAuth() {
		return true
	}
	return s.cfg.APIAuth.Token != "" ||
		s.cfg.APIAuth.WriteToken != "" ||
		s.cfg.APIAuth.Username != "" ||
		s.cfg.APIAuth.Password != ""
}

func (s *Server) apiAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Webhook.Enabled && strings.HasPrefix(r.URL.Path, "/api/webhooks/") {
			next.ServeHTTP(w, r)
			return
		}
		if s.useExternalAuth() {
			// Keep health checks simple for probes and local diagnostics.
			if r.URL.Path == "/api/health" {
				next.ServeHTTP(w, r)
				return
			}
			s.externalRoleMiddleware(roleViewer)(next).ServeHTTP(w, r)
			return
		}

		if s.cfg.APIAuth.Token != "" {
			token := r.Header.Get(s.cfg.APIAuth.TokenHeader)
			if token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.APIAuth.Token)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}
		// Write token can also authenticate read requests for operational simplicity.
		if s.cfg.APIAuth.WriteToken != "" {
			writeToken := r.Header.Get(s.cfg.APIAuth.WriteTokenHeader)
			if writeToken != "" && subtle.ConstantTimeCompare([]byte(writeToken), []byte(s.cfg.APIAuth.WriteToken)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}

		if s.apiBasicAuthorized(r) {
			next.ServeHTTP(w, r)
			return
		}

		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}

func (s *Server) settingsAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.useExternalAuth() {
			s.externalRoleMiddleware(roleAdmin)(next).ServeHTTP(w, r)
			return
		}

		if s.apiAuthEnabled() {
			s.apiAuthMiddleware(next).ServeHTTP(w, r)
			return
		}

		if s.cfg.UIAuth.Username != "" || s.cfg.UIAuth.Password != "" {
			if s.uiBasicAuthorized(r) {
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

func (s *Server) apiWriteAuthEnabled() bool {
	if s.useExternalAuth() {
		return true
	}
	return s.cfg.APIAuth.WriteToken != ""
}

// apiWriteAuthMiddleware protects mutating API routes. If write_token is configured,
// write requests require write token auth (or API basic auth).
func (s *Server) apiWriteAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.useExternalAuth() {
			required := roleOperator
			if strings.HasPrefix(r.URL.Path, "/api/settings/") {
				required = roleAdmin
			}
			s.externalRoleMiddleware(required)(next).ServeHTTP(w, r)
			return
		}

		if !s.apiWriteAuthEnabled() {
			next.ServeHTTP(w, r)
			return
		}

		writeToken := r.Header.Get(s.cfg.APIAuth.WriteTokenHeader)
		if writeToken != "" && subtle.ConstantTimeCompare([]byte(writeToken), []byte(s.cfg.APIAuth.WriteToken)) == 1 {
			next.ServeHTTP(w, r)
			return
		}

		// Allow API basic auth as an administrative override.
		if s.apiBasicAuthorized(r) {
			next.ServeHTTP(w, r)
			return
		}

		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}

func (s *Server) securityHeadersMiddleware(next http.Handler) http.Handler {
	const csp = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com data:; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		w.Header().Set("Content-Security-Policy", csp)
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) apiBasicAuthorized(r *http.Request) bool {
	if s.cfg.APIAuth.Username == "" && s.cfg.APIAuth.Password == "" {
		return false
	}
	username, password, ok := r.BasicAuth()
	return ok &&
		subtle.ConstantTimeCompare([]byte(username), []byte(s.cfg.APIAuth.Username)) == 1 &&
		subtle.ConstantTimeCompare([]byte(password), []byte(s.cfg.APIAuth.Password)) == 1
}

func (s *Server) uiBasicAuthorized(r *http.Request) bool {
	if s.cfg.UIAuth.Username == "" && s.cfg.UIAuth.Password == "" {
		return false
	}
	username, password, ok := r.BasicAuth()
	return ok &&
		subtle.ConstantTimeCompare([]byte(username), []byte(s.cfg.UIAuth.Username)) == 1 &&
		subtle.ConstantTimeCompare([]byte(password), []byte(s.cfg.UIAuth.Password)) == 1
}

func (s *Server) csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := s.ensureCSRFToken(w, r)
		ctx := context.WithValue(r.Context(), csrfContextKey, token)

		if r.Method == http.MethodPost && !s.shouldBypassCSRFCheck(r) {
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
		ip := s.clientIP(r)
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
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	return token
}

func (s *Server) shouldBypassCSRFCheck(r *http.Request) bool {
	return s != nil && s.cfg != nil && s.cfg.InsecureDevMode && !isHTTPSRequest(r)
}

func isHTTPSRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
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

func (s *Server) clientIP(r *http.Request) string {
	peerIP := peerIPFromRemoteAddr(r.RemoteAddr)
	trustProxy := peerIP != nil && (peerIP.IsLoopback() || peerIP.IsPrivate())
	if s != nil && s.cfg != nil && s.cfg.API.TrustProxy {
		trustProxy = true
	}

	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded != "" && trustProxy {
		parts := strings.Split(forwarded, ",")
		return strings.TrimSpace(parts[0])
	}

	realIP := r.Header.Get("X-Real-IP")
	if realIP != "" && trustProxy {
		return realIP
	}

	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

func peerIPFromRemoteAddr(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		// remoteAddr may already be just a host.
		host = remoteAddr
	}
	return net.ParseIP(host)
}

func (s *Server) getRateLimiter(ip string) *rate.Limiter {
	s.rateLimitMu.Lock()
	defer s.rateLimitMu.Unlock()

	if entry, ok := s.rateLimiters[ip]; ok {
		entry.lastSeen = time.Now()
		return entry.limiter
	}

	ratePerMinute := 60
	if s.cfg.API.RateLimitPerMinute > 0 {
		ratePerMinute = s.cfg.API.RateLimitPerMinute
	}
	limit := rate.Limit(float64(ratePerMinute) / 60.0)
	burst := ratePerMinute
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
