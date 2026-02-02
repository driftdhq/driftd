package api

import (
	"html/template"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
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
