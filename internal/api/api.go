package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/gitauth"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/stack"
	"github.com/driftdhq/driftd/internal/storage"
	"github.com/driftdhq/driftd/internal/version"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"golang.org/x/time/rate"
)

type Server struct {
	cfg       *config.Config
	storage   *storage.Storage
	queue     *queue.Queue
	tmplIndex *template.Template
	tmplRepo  *template.Template
	tmplDrift *template.Template
	staticFS  fs.FS

	rateLimitMu  sync.Mutex
	rateLimiters map[string]*rateLimiterEntry
}

type rateLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

const csrfCookieName = "driftd_csrf"

var repoNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func New(cfg *config.Config, s *storage.Storage, q *queue.Queue, templatesFS, staticFS fs.FS) (*Server, error) {
	funcMap := template.FuncMap{
		"timeAgo": timeAgo,
		"pluralize": func(singular, plural string, count int) string {
			if count == 1 {
				return singular
			}
			return plural
		},
		"commitURL": commitURL,
		"add": func(a, b int) int {
			return a + b
		},
		"mul": func(a, b int) int {
			return a * b
		},
		"div": func(a, b int) int {
			if b == 0 {
				return 0
			}
			return a / b
		},
	}

	tmplIndex, err := template.New("").Funcs(funcMap).ParseFS(templatesFS, "templates/layout.html", "templates/index.html")
	if err != nil {
		return nil, err
	}
	tmplRepo, err := template.New("").Funcs(funcMap).ParseFS(templatesFS, "templates/layout.html", "templates/repo.html")
	if err != nil {
		return nil, err
	}
	tmplDrift, err := template.New("").Funcs(funcMap).ParseFS(templatesFS, "templates/layout.html", "templates/drift.html")
	if err != nil {
		return nil, err
	}

	return &Server{
		cfg:          cfg,
		storage:      s,
		queue:        q,
		tmplIndex:    tmplIndex,
		tmplRepo:     tmplRepo,
		tmplDrift:    tmplDrift,
		staticFS:     staticFS,
		rateLimiters: make(map[string]*rateLimiterEntry),
	}, nil
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// HTML routes
	r.Group(func(r chi.Router) {
		if s.cfg.UIAuth.Username != "" || s.cfg.UIAuth.Password != "" {
			r.Use(s.uiAuthMiddleware)
		}
		r.Use(s.csrfMiddleware)
		r.Get("/", s.handleIndex)
		r.Get("/repos/{repo}", s.handleRepo)
		r.Post("/repos/{repo}/scan", s.handleScanRepoUI)
		r.Get("/repos/{repo}/stacks/*", s.handleStack)
		r.Post("/repos/{repo}/stacks/*", s.handleScanStack)
	})

	// API routes
	r.Route("/api", func(r chi.Router) {
		if s.apiAuthEnabled() {
			r.Use(s.apiAuthMiddleware)
		}
		r.Get("/health", s.handleHealth)
		r.Get("/stacks/{stackID}", s.handleGetStackScan)
		r.Get("/scans/{scanID}", s.handleGetScan)
		r.Get("/repos/{repo}/stacks", s.handleListRepoStackScans)
		r.With(s.rateLimitMiddleware).Post("/repos/{repo}/scan", s.handleScanRepo)
		r.With(s.rateLimitMiddleware).Post("/repos/{repo}/stacks/*", s.handleScanStack)
		if s.cfg.Webhook.Enabled {
			r.Post("/webhooks/github", s.handleGitHubWebhook)
		}
	})

	// Static files from embedded FS
	staticHandler, _ := fs.Sub(s.staticFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticHandler))))

	return r
}

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

type contextKey string

const csrfContextKey contextKey = "csrf"

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
		if s.cfg.API.RateLimitPerMinute <= 0 {
			next.ServeHTTP(w, r)
			return
		}

		ip := clientIP(r)
		if ip == "" {
			ip = "unknown"
		}
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
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		return xr
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) getRateLimiter(ip string) *rate.Limiter {
	s.rateLimitMu.Lock()
	defer s.rateLimitMu.Unlock()

	now := time.Now()
	if entry, ok := s.rateLimiters[ip]; ok {
		entry.lastSeen = now
		return entry.limiter
	}

	perMin := s.cfg.API.RateLimitPerMinute
	limit := rate.Every(time.Minute / time.Duration(perMin))
	burst := perMin
	if burst < 1 {
		burst = 1
	}
	limiter := rate.NewLimiter(limit, burst)
	s.rateLimiters[ip] = &rateLimiterEntry{limiter: limiter, lastSeen: now}

	if len(s.rateLimiters) > 1000 {
		cutoff := now.Add(-10 * time.Minute)
		for key, entry := range s.rateLimiters {
			if entry.lastSeen.Before(cutoff) {
				delete(s.rateLimiters, key)
			}
		}
	}
	return limiter
}

type indexData struct {
	Repos        []repoStatusData
	ConfigRepos  []config.RepoConfig
	RepoByName   map[string]repoStatusData
	ConfigByName map[string]config.RepoConfig
	TotalRepos   int
	TotalStacks  int
	DriftedRepos int
	ActiveScans  int
	LockedRepos  int
}

type repoStatusData struct {
	Name          string
	Drifted       bool
	Stacks        int
	DriftedStacks int
	Locked        bool
	LastRun       time.Time
	CommitSHA     string
	Active        bool
	Progress      string
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	repos, _ := s.storage.ListRepos()

	var repoData []repoStatusData
	for _, repo := range repos {
		locked, _ := s.queue.IsRepoLocked(r.Context(), repo.Name)
		var lastScan *queue.Scan
		if activeScan, err := s.queue.GetActiveScan(r.Context(), repo.Name); err == nil {
			lastScan = activeScan
		} else if lastScanFound, err := s.queue.GetLastScan(r.Context(), repo.Name); err == nil {
			lastScan = lastScanFound
		}

		var progress string
		var active bool
		var lastRun time.Time
		var commit string
		if lastScan != nil {
			commit = lastScan.CommitSHA
			if lastScan.Status == queue.ScanStatusRunning {
				active = true
				progress = fmt.Sprintf("%d/%d", lastScan.Completed+lastScan.Failed, lastScan.Total)
				lastRun = lastScan.StartedAt
			} else {
				lastRun = lastScan.EndedAt
			}
		}
		repoData = append(repoData, repoStatusData{
			Name:          repo.Name,
			Drifted:       repo.Drifted,
			Stacks:        repo.Stacks,
			DriftedStacks: repo.DriftedStacks,
			Locked:        locked,
			LastRun:       lastRun,
			CommitSHA:     commit,
			Active:        active,
			Progress:      progress,
		})
	}

	totalStacks := 0
	driftedRepos := 0
	activeScans := 0
	lockedRepos := 0
	for _, repo := range repoData {
		totalStacks += repo.Stacks
		if repo.Drifted {
			driftedRepos++
		}
		if repo.Active {
			activeScans++
		}
		if repo.Locked {
			lockedRepos++
		}
	}

	data := indexData{
		Repos:        repoData,
		ConfigRepos:  s.cfg.Repos,
		RepoByName:   map[string]repoStatusData{},
		ConfigByName: map[string]config.RepoConfig{},
		TotalRepos:   len(s.cfg.Repos),
		TotalStacks:  totalStacks,
		DriftedRepos: driftedRepos,
		ActiveScans:  activeScans,
		LockedRepos:  lockedRepos,
	}
	for _, repo := range repoData {
		data.RepoByName[repo.Name] = repo
	}
	for _, repo := range s.cfg.Repos {
		data.ConfigByName[repo.Name] = repo
	}

	if err := s.tmplIndex.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("template error: %v", err)
	}
}

type repoPageData struct {
	Name       string
	Stacks     []storage.StackStatus
	StackTree  []*stackNode
	Config     *config.RepoConfig
	Locked     bool
	ActiveScan *queue.Scan
	LastScan   *queue.Scan
	CSRFToken  string
}

func (s *Server) handleRepo(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	if !isValidRepoName(repoName) {
		http.Error(w, "Invalid repository name", http.StatusBadRequest)
		return
	}

	stacks, _ := s.storage.ListStacks(repoName)
	csrfToken := csrfTokenFromContext(r.Context())
	stackTree := buildStackTree(repoName, stacks, csrfToken)
	repoCfg := s.cfg.GetRepo(repoName)
	locked, _ := s.queue.IsRepoLocked(r.Context(), repoName)
	activeScan, _ := s.queue.GetActiveScan(r.Context(), repoName)
	lastScan, _ := s.queue.GetLastScan(r.Context(), repoName)

	data := repoPageData{
		Name:       repoName,
		Stacks:     stacks,
		StackTree:  stackTree,
		Config:     repoCfg,
		Locked:     locked,
		ActiveScan: activeScan,
		LastScan:   lastScan,
		CSRFToken:  csrfToken,
	}

	if err := s.tmplRepo.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("template error: %v", err)
	}
}

type stackPageData struct {
	RepoName  string
	RepoURL   string
	Path      string
	Result    *storage.RunResult
	Scan      *queue.Scan
	CSRFToken string
	PlanHTML  template.HTML
}

func (s *Server) handleStack(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	stackPath := chi.URLParam(r, "*")
	if !isValidRepoName(repoName) || !isSafeStackPath(stackPath) {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	repoCfg := s.cfg.GetRepo(repoName)
	result, err := s.storage.GetResult(repoName, stackPath)
	if err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}
	lastScan, _ := s.queue.GetLastScan(r.Context(), repoName)

	data := stackPageData{
		RepoName:  repoName,
		RepoURL:   "",
		Path:      stackPath,
		Result:    result,
		Scan:      lastScan,
		CSRFToken: csrfTokenFromContext(r.Context()),
		PlanHTML:  formatPlanOutput(result.PlanOutput),
	}
	if repoCfg != nil {
		data.RepoURL = repoCfg.URL
	}

	if err := s.tmplDrift.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("template error: %v", err)
	}
}

// API Handlers

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Check Redis connection
	if err := s.queue.Client().Ping(r.Context()).Err(); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"status": "unhealthy", "error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
}

func (s *Server) handleGetStackScan(w http.ResponseWriter, r *http.Request) {
	stackID := chi.URLParam(r, "stackID")

	stackScan, err := s.queue.GetStackScan(r.Context(), stackID)
	if err != nil {
		if err == queue.ErrStackScanNotFound {
			http.Error(w, "Stack scan not found", http.StatusNotFound)
			return
		}
		http.Error(w, s.sanitizeErrorMessage(err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stackScan)
}

func (s *Server) handleListRepoStackScans(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	if !isValidRepoName(repoName) {
		http.Error(w, "Invalid repository name", http.StatusBadRequest)
		return
	}

	stackScans, err := s.queue.ListRepoStackScans(r.Context(), repoName, 50)
	if err != nil {
		http.Error(w, s.sanitizeErrorMessage(err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stackScans)
}

type scanRequest struct {
	Trigger string `json:"trigger,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Actor   string `json:"actor,omitempty"`
}

type scanResponse struct {
	Stacks     []string    `json:"stacks,omitempty"`
	Scan       *queue.Scan `json:"scan,omitempty"`
	ActiveScan *queue.Scan `json:"active_scan,omitempty"`
	Message    string      `json:"message,omitempty"`
	Error      string      `json:"error,omitempty"`
}

func (s *Server) handleScanRepoUI(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	if !isValidRepoName(repoName) {
		http.Error(w, "Invalid repository name", http.StatusBadRequest)
		return
	}

	repoCfg := s.cfg.GetRepo(repoName)
	if repoCfg == nil {
		http.Error(w, "Repository not configured", http.StatusNotFound)
		return
	}

	trigger := "manual"
	maxRetries := 0
	if s.cfg.Worker.RetryOnce {
		maxRetries = 1
	}

	scan, stacks, err := s.startScanWithCancel(r.Context(), repoCfg, trigger, "", "")
	if err != nil {
		if err == queue.ErrRepoLocked {
			http.Redirect(w, r, "/repos/"+repoName, http.StatusSeeOther)
			return
		}
		http.Error(w, s.sanitizeErrorMessage(err.Error()), http.StatusInternalServerError)
		return
	}

	for _, stackPath := range stacks {
		job := &queue.StackScan{
			ScanID:     scan.ID,
			RepoName:   repoName,
			RepoURL:    repoCfg.URL,
			StackPath:  stackPath,
			MaxRetries: maxRetries,
			Trigger:    trigger,
		}
		if err := s.queue.Enqueue(r.Context(), job); err != nil {
			_ = s.queue.MarkScanEnqueueFailed(r.Context(), scan.ID)
			break
		}
	}

	http.Redirect(w, r, "/repos/"+repoName, http.StatusSeeOther)
}

func (s *Server) handleScanRepo(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	if !isValidRepoName(repoName) {
		http.Error(w, "Invalid repository name", http.StatusBadRequest)
		return
	}

	repoCfg := s.cfg.GetRepo(repoName)
	if repoCfg == nil {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(scanResponse{Error: "Repository not configured"})
		return
	}

	var req scanRequest
	if r.Body != nil && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}
	}
	if req.Trigger == "" {
		req.Trigger = "manual"
	}

	maxRetries := 0
	if s.cfg.Worker.RetryOnce {
		maxRetries = 1
	}

	scan, stacks, err := s.startScanWithCancel(r.Context(), repoCfg, req.Trigger, req.Commit, req.Actor)
	if err != nil {
		if err == queue.ErrRepoLocked {
			activeScan, activeErr := s.queue.GetActiveScan(r.Context(), repoName)
			if activeErr != nil {
				http.Error(w, "Repository scan already in progress", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(scanResponse{
				Error:      "Repository scan already in progress",
				ActiveScan: activeScan,
			})
			return
		}
		http.Error(w, s.sanitizeErrorMessage(err.Error()), http.StatusInternalServerError)
		return
	}
	// startScanWithCancel handles lock renewal and version detection

	var stackIDs []string
	var errors []string

	for _, stackPath := range stacks {
		stackScan := &queue.StackScan{
			ScanID:     scan.ID,
			RepoName:   repoName,
			RepoURL:    repoCfg.URL,
			StackPath:  stackPath,
			MaxRetries: maxRetries,
			Trigger:    req.Trigger,
			Commit:     req.Commit,
			Actor:      req.Actor,
		}

		if err := s.queue.Enqueue(r.Context(), stackScan); err != nil {
			_ = s.queue.MarkScanEnqueueFailed(r.Context(), scan.ID)
			if err == queue.ErrRepoLocked {
				errors = append(errors, fmt.Sprintf("%s: repo locked", stackPath))
			} else {
				errors = append(errors, fmt.Sprintf("%s: %s", stackPath, s.sanitizeErrorMessage(err.Error())))
			}
			continue
		}
		stackIDs = append(stackIDs, stackScan.ID)
	}

	w.Header().Set("Content-Type", "application/json")

	if len(stackIDs) == 0 && len(errors) > 0 {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(scanResponse{
			Error:   "Failed to enqueue any stacks",
			Message: strings.Join(errors, "; "),
		})
		return
	}

	resp := scanResponse{
		Stacks:  stackIDs,
		Scan:    scan,
		Message: fmt.Sprintf("Enqueued %d stacks", len(stackIDs)),
	}
	if len(errors) > 0 {
		resp.Error = strings.Join(errors, "; ")
	}

	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleScanStack(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	stackPath := chi.URLParam(r, "*")
	if !isValidRepoName(repoName) || !isSafeStackPath(stackPath) {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	repoCfg := s.cfg.GetRepo(repoName)
	if repoCfg == nil {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(scanResponse{Error: "Repository not configured"})
		return
	}

	var req scanRequest
	if r.Body != nil && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}
	}
	if req.Trigger == "" {
		req.Trigger = "manual"
	}

	maxRetries := 0
	if s.cfg.Worker.RetryOnce {
		maxRetries = 1
	}

	scan, stacks, err := s.startScanWithCancel(r.Context(), repoCfg, req.Trigger, req.Commit, req.Actor)
	if err != nil {
		if err == queue.ErrRepoLocked {
			activeScan, activeErr := s.queue.GetActiveScan(r.Context(), repoName)
			if activeErr != nil {
				http.Error(w, "Repository scan already in progress", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(scanResponse{
				Error:      "Repository scan already in progress",
				ActiveScan: activeScan,
			})
			return
		}
		http.Error(w, s.sanitizeErrorMessage(err.Error()), http.StatusInternalServerError)
		return
	}
	// startScanWithCancel handles lock renewal and version detection

	if !containsStack(stackPath, stacks) {
		_ = s.queue.FailScan(r.Context(), scan.ID, repoName, "stack not found")
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}
	_ = s.queue.SetScanTotal(r.Context(), scan.ID, 1)

	stackScan := &queue.StackScan{
		ScanID:     scan.ID,
		RepoName:   repoName,
		RepoURL:    repoCfg.URL,
		StackPath:  stackPath,
		MaxRetries: maxRetries,
		Trigger:    req.Trigger,
		Commit:     req.Commit,
		Actor:      req.Actor,
	}

	if err := s.queue.Enqueue(r.Context(), stackScan); err != nil {
		_ = s.queue.MarkScanEnqueueFailed(r.Context(), scan.ID)
		w.Header().Set("Content-Type", "application/json")
		if err == queue.ErrRepoLocked {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(scanResponse{Error: "Repository scan already in progress"})
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(scanResponse{Error: s.sanitizeErrorMessage(err.Error())})
		}
		return
	}

	// Redirect for form POSTs (UI), return JSON for API
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Redirect(w, r, fmt.Sprintf("/repos/%s/stacks/%s", repoName, stackPath), http.StatusSeeOther)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(scanResponse{
		Stacks:  []string{stackScan.ID},
		Scan:    scan,
		Message: "Stack enqueued",
	})
}

type gitHubPushPayload struct {
	Ref        string `json:"ref"`
	Repository struct {
		Name          string `json:"name"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
	HeadCommit struct {
		ID string `json:"id"`
	} `json:"head_commit"`
	Pusher struct {
		Name string `json:"name"`
	} `json:"pusher"`
	Commits []struct {
		Added    []string `json:"added"`
		Modified []string `json:"modified"`
		Removed  []string `json:"removed"`
	} `json:"commits"`
}

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	if !s.validateWebhookRequest(w, r, body) {
		return
	}
	if event := r.Header.Get("X-GitHub-Event"); event != "" && event != "push" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	var payload gitHubPushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	repoName := payload.Repository.Name
	repoCfg := s.cfg.GetRepo(repoName)
	if repoCfg == nil && payload.Repository.FullName != "" {
		parts := strings.Split(payload.Repository.FullName, "/")
		if len(parts) > 0 {
			repoCfg = s.cfg.GetRepo(parts[len(parts)-1])
			repoName = parts[len(parts)-1]
		}
	}
	if repoCfg == nil {
		http.Error(w, "Repository not configured", http.StatusNotFound)
		return
	}

	defaultBranch := payload.Repository.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	if payload.Ref != "refs/heads/"+defaultBranch {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	changedFiles := extractChangedFiles(payload, s.cfg.Webhook.MaxFiles)
	if len(changedFiles) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	trigger := "webhook"
	scan, stacks, err := s.startScanWithCancel(r.Context(), repoCfg, trigger, payload.HeadCommit.ID, payload.Pusher.Name)
	if err != nil {
		if err == queue.ErrRepoLocked {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(scanResponse{Error: "Repository scan already in progress"})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	targetStacks := selectStacksForChanges(stacks, changedFiles)
	if len(targetStacks) == 0 {
		_ = s.queue.FailScan(r.Context(), scan.ID, repoName, "no matching stacks for webhook changes")
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if err := s.queue.SetScanTotal(r.Context(), scan.ID, len(targetStacks)); err != nil {
		_ = s.queue.FailScan(r.Context(), scan.ID, repoName, fmt.Sprintf("failed to set scan total: %v", err))
		http.Error(w, "Failed to set scan total", http.StatusInternalServerError)
		return
	}

	maxRetries := 0
	if s.cfg.Worker.RetryOnce {
		maxRetries = 1
	}

	var stackIDs []string
	for _, stackPath := range targetStacks {
		stackScan := &queue.StackScan{
			ScanID:     scan.ID,
			RepoName:   repoName,
			RepoURL:    repoCfg.URL,
			StackPath:  stackPath,
			MaxRetries: maxRetries,
			Trigger:    trigger,
			Commit:     payload.HeadCommit.ID,
			Actor:      payload.Pusher.Name,
		}
		if err := s.queue.Enqueue(r.Context(), stackScan); err != nil {
			_ = s.queue.MarkScanEnqueueFailed(r.Context(), scan.ID)
			continue
		}
		stackIDs = append(stackIDs, stackScan.ID)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(scanResponse{
		Stacks:  stackIDs,
		Scan:    scan,
		Message: fmt.Sprintf("Enqueued %d stacks", len(stackIDs)),
	})
}

func extractChangedFiles(payload gitHubPushPayload, maxFiles int) []string {
	seen := map[string]struct{}{}
	var files []string
	for _, commit := range payload.Commits {
		for _, path := range append(append(commit.Added, commit.Modified...), commit.Removed...) {
			path = strings.TrimPrefix(path, "/")
			if path == "" {
				continue
			}
			if !isInfraFile(path) {
				continue
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			files = append(files, filepath.ToSlash(path))
			if maxFiles > 0 && len(files) >= maxFiles {
				return files
			}
		}
	}
	return files
}

func selectStacksForChanges(stacks []string, changedFiles []string) []string {
	if len(stacks) == 0 || len(changedFiles) == 0 {
		return nil
	}

	stackSet := map[string]struct{}{}
	for _, stackPath := range stacks {
		stackSet[filepath.ToSlash(stackPath)] = struct{}{}
	}

	target := map[string]struct{}{}
	for _, filePath := range changedFiles {
		dir := filepath.ToSlash(filepath.Dir(filePath))
		matched := false
		for dir != "." && dir != "" {
			if _, ok := stackSet[dir]; ok {
				target[dir] = struct{}{}
				matched = true
				break
			}
			dir = filepath.ToSlash(filepath.Dir(dir))
		}
		if !matched {
			if _, ok := stackSet[""]; ok && filepath.Dir(filePath) == "." {
				target[""] = struct{}{}
				matched = true
			}
		}
		_ = matched
	}

	var out []string
	for stack := range target {
		out = append(out, stack)
	}
	sort.Strings(out)
	return out
}

func isInfraFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if base == "terragrunt.hcl" {
		return true
	}
	if strings.HasSuffix(base, ".tf") ||
		strings.HasSuffix(base, ".tf.json") ||
		strings.HasSuffix(base, ".tfvars") ||
		strings.HasSuffix(base, ".tfvars.json") ||
		strings.HasSuffix(base, ".hcl") {
		return true
	}
	return false
}

func (s *Server) validateWebhookRequest(w http.ResponseWriter, r *http.Request, body []byte) bool {
	if s.cfg.Webhook.GitHubSecret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		if sig == "" {
			http.Error(w, "Missing signature", http.StatusUnauthorized)
			return false
		}
		const prefix = "sha256="
		if !strings.HasPrefix(sig, prefix) {
			http.Error(w, "Invalid signature format", http.StatusUnauthorized)
			return false
		}
		expected := computeHMACSHA256(body, []byte(s.cfg.Webhook.GitHubSecret))
		got, err := hex.DecodeString(strings.TrimPrefix(sig, prefix))
		if err != nil || !hmac.Equal(got, expected) {
			http.Error(w, "Invalid signature", http.StatusUnauthorized)
			return false
		}
		return true
	}

	if s.cfg.Webhook.Token != "" {
		token := r.Header.Get(s.cfg.Webhook.TokenHeader)
		if token == "" || token != s.cfg.Webhook.Token {
			http.Error(w, "Invalid token", http.StatusUnauthorized)
			return false
		}
		return true
	}

	http.Error(w, "Webhook not configured", http.StatusUnauthorized)
	return false
}

func computeHMACSHA256(payload, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return mac.Sum(nil)
}

func (s *Server) handleGetScan(w http.ResponseWriter, r *http.Request) {
	scanID := chi.URLParam(r, "scanID")

	scan, err := s.queue.GetScan(r.Context(), scanID)
	if err != nil {
		if err == queue.ErrScanNotFound {
			http.Error(w, "Scan not found", http.StatusNotFound)
			return
		}
		http.Error(w, s.sanitizeErrorMessage(err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(scan)
}

func (s *Server) startScanWithCancel(ctx context.Context, repoCfg *config.RepoConfig, trigger, commit, actor string) (*queue.Scan, []string, error) {
	if repoCfg == nil {
		return nil, nil, fmt.Errorf("repository not configured")
	}

	scan, err := s.queue.StartScan(ctx, repoCfg.Name, trigger, commit, actor, 0)
	if err != nil && err == queue.ErrRepoLocked {
		activeScan, activeErr := s.queue.GetActiveScan(ctx, repoCfg.Name)
		if repoCfg.CancelInflightEnabled() && activeErr == nil && activeScan != nil {
			newPriority := queue.TriggerPriority(trigger)
			activePriority := queue.TriggerPriority(activeScan.Trigger)
			if newPriority >= activePriority {
				_ = s.queue.CancelScan(ctx, activeScan.ID, repoCfg.Name, "superseded by new trigger")
				scan, err = s.queue.StartScan(ctx, repoCfg.Name, trigger, commit, actor, 0)
			}
		}
	}
	if err != nil {
		return nil, nil, err
	}

	// Use Background context because renewal must continue independent of the HTTP request.
	// The goroutine exits when scan status changes to completed/failed/canceled.
	go s.queue.RenewScanLock(context.Background(), scan.ID, repoCfg.Name, s.cfg.Worker.ScanMaxAge, s.cfg.Worker.RenewEvery)

	auth, err := gitauth.AuthMethod(ctx, repoCfg)
	if err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, err.Error())
		return nil, nil, err
	}

	workspacePath, commitSHA, err := s.cloneWorkspace(ctx, repoCfg, scan.ID, auth)
	if err != nil {
		if workspacePath != "" {
			_ = os.RemoveAll(filepath.Dir(workspacePath))
		}
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, err.Error())
		return nil, nil, err
	}
	if err := s.queue.SetScanWorkspace(ctx, scan.ID, workspacePath, commitSHA); err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, fmt.Sprintf("failed to set workspace: %v", err))
		return nil, nil, err
	}
	go s.cleanupWorkspaces(repoCfg.Name, scan.ID)

	stacks, err := stack.Discover(workspacePath, repoCfg.IgnorePaths)
	if err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, err.Error())
		return nil, nil, err
	}
	if len(stacks) == 0 {
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, "no stacks discovered")
		return nil, nil, fmt.Errorf("no stacks discovered")
	}

	versions, err := version.Detect(workspacePath, stacks)
	if err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, err.Error())
		return nil, nil, err
	}
	if err := s.queue.SetScanVersions(ctx, scan.ID, versions.DefaultTerraform, versions.DefaultTerragrunt, versions.StackTerraform, versions.StackTerragrunt); err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, fmt.Sprintf("failed to set versions: %v", err))
		return nil, nil, err
	}
	if err := s.queue.SetScanTotal(ctx, scan.ID, len(stacks)); err != nil {
		_ = s.queue.FailScan(ctx, scan.ID, repoCfg.Name, fmt.Sprintf("failed to set scan total: %v", err))
		return nil, nil, err
	}

	return scan, stacks, nil
}

func (s *Server) cloneWorkspace(ctx context.Context, repoCfg *config.RepoConfig, scanID string, auth transport.AuthMethod) (string, string, error) {
	base := filepath.Join(s.cfg.DataDir, "workspaces", repoCfg.Name, scanID, "repo")
	if err := os.MkdirAll(filepath.Dir(base), 0755); err != nil {
		return base, "", err
	}

	cloneOpts := &git.CloneOptions{
		URL:   repoCfg.URL,
		Depth: 1,
		Auth:  auth,
	}
	if repoCfg.Branch != "" {
		cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(repoCfg.Branch)
		cloneOpts.SingleBranch = true
	}
	cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	repo, err := git.PlainCloneContext(cloneCtx, base, false, cloneOpts)
	if err != nil {
		return base, "", err
	}

	head, err := repo.Head()
	if err != nil {
		return base, "", nil
	}
	return base, head.Hash().String(), nil
}

func (s *Server) cleanupWorkspaces(repoName, keepScanID string) {
	retention := s.cfg.Workspace.Retention
	if retention <= 0 {
		return
	}

	base := filepath.Join(s.cfg.DataDir, "workspaces", repoName)
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}

	type item struct {
		id   string
		path string
		mod  time.Time
	}
	var items []item
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		if id == keepScanID {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		items = append(items, item{
			id:   id,
			path: filepath.Join(base, id),
			mod:  info.ModTime(),
		})
	}

	if len(items) <= retention-1 {
		return
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].mod.After(items[j].mod)
	})

	toDelete := items[retention-1:]
	for _, it := range toDelete {
		scan, err := s.queue.GetScan(context.Background(), it.id)
		if err == nil && scan != nil && scan.Status == queue.ScanStatusRunning {
			continue
		}
		// Note: There's a small race window where scan status could change between
		// the check and RemoveAll. This is acceptable because workers copy the
		// workspace to a temp directory before processing, so deletion during
		// processing won't affect the running stack scan.
		_ = os.RemoveAll(it.path)
	}
}

func containsStack(target string, stacks []string) bool {
	for _, s := range stacks {
		if s == target {
			return true
		}
	}
	return false
}

func isValidRepoName(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	return repoNamePattern.MatchString(name)
}

func isSafeStackPath(stackPath string) bool {
	if stackPath == "" {
		return true
	}
	if filepath.IsAbs(stackPath) {
		return false
	}
	clean := filepath.Clean(stackPath)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}

func (s *Server) sanitizeErrorMessage(msg string) string {
	if s.cfg != nil && s.cfg.DataDir != "" {
		msg = strings.ReplaceAll(msg, s.cfg.DataDir, "<data-dir>")
	}
	tmp := os.TempDir()
	if tmp != "" {
		msg = strings.ReplaceAll(msg, tmp, "<tmp>")
	}
	return msg
}

func formatPlanOutput(plan string) template.HTML {
	if plan == "" {
		return ""
	}
	clean := ansiEscapePattern.ReplaceAllString(plan, "")
	lines := strings.Split(clean, "\n")
	var b strings.Builder
	for i, line := range lines {
		class := planLineClass(line)
		escaped := html.EscapeString(line)
		if class != "" {
			b.WriteString(`<span class="plan-line `)
			b.WriteString(class)
			b.WriteString(`">`)
			b.WriteString(escaped)
			b.WriteString(`</span>`)
		} else {
			b.WriteString(escaped)
		}
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	return template.HTML(b.String())
}

func planLineClass(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return ""
	}
	switch trimmed[0] {
	case '+':
		if strings.HasPrefix(trimmed, "+++") {
			return ""
		}
		return "plan-add"
	case '-':
		if strings.HasPrefix(trimmed, "---") {
			return ""
		}
		return "plan-remove"
	case '~':
		return "plan-change"
	default:
		return ""
	}
}

func commitURL(repoURL, sha string) string {
	if repoURL == "" || sha == "" {
		return ""
	}
	clean := strings.TrimSuffix(repoURL, ".git")
	switch {
	case strings.HasPrefix(clean, "git@github.com:"):
		clean = strings.TrimPrefix(clean, "git@github.com:")
		return "https://github.com/" + clean + "/commit/" + sha
	case strings.HasPrefix(clean, "git@gitlab.com:"):
		clean = strings.TrimPrefix(clean, "git@gitlab.com:")
		return "https://gitlab.com/" + clean + "/-/commit/" + sha
	case strings.HasPrefix(clean, "https://github.com/"):
		return clean + "/commit/" + sha
	case strings.HasPrefix(clean, "http://github.com/"):
		return strings.Replace(clean, "http://github.com/", "https://github.com/", 1) + "/commit/" + sha
	case strings.HasPrefix(clean, "https://gitlab.com/"):
		return clean + "/-/commit/" + sha
	case strings.HasPrefix(clean, "http://gitlab.com/"):
		return strings.Replace(clean, "http://gitlab.com/", "https://gitlab.com/", 1) + "/-/commit/" + sha
	default:
		return ""
	}
}

func timeAgo(t time.Time) string {
	if t.IsZero() {
		return "never"
	}

	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", h)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
}
