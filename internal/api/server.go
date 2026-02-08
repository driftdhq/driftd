package api

import (
	"html/template"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/metrics"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/repos"
	"github.com/driftdhq/driftd/internal/secrets"
	"github.com/driftdhq/driftd/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"
)

type Server struct {
	cfg          *config.Config
	storage      *storage.Storage
	queue        *queue.Queue
	repoStore    *secrets.RepoStore
	intStore     *secrets.IntegrationStore
	repoProvider repos.Provider
	tmplIndex    *template.Template
	tmplRepo     *template.Template
	tmplDrift    *template.Template
	tmplSettings *template.Template
	staticFS     fs.FS

	rateLimitMu  sync.Mutex
	rateLimiters map[string]*rateLimiterEntry

	onRepoAdded   func(name, schedule string)
	onRepoUpdated func(name, schedule string)
	onRepoDeleted func(name string)
}

type rateLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// ServerOption is a functional option for configuring the Server.
type ServerOption func(*Server)

// WithRepoStore sets the dynamic repository store.
func WithRepoStore(rs *secrets.RepoStore) ServerOption {
	return func(s *Server) {
		s.repoStore = rs
	}
}

// WithIntegrationStore sets the integration store.
func WithIntegrationStore(is *secrets.IntegrationStore) ServerOption {
	return func(s *Server) {
		s.intStore = is
	}
}

// WithRepoProvider sets a repository provider for resolving dynamic repos.
func WithRepoProvider(provider repos.Provider) ServerOption {
	return func(s *Server) {
		s.repoProvider = provider
	}
}

// WithSchedulerCallbacks sets callbacks for scheduler integration when repos change.
func WithSchedulerCallbacks(onAdded, onUpdated func(name, schedule string), onDeleted func(name string)) ServerOption {
	return func(s *Server) {
		s.onRepoAdded = onAdded
		s.onRepoUpdated = onUpdated
		s.onRepoDeleted = onDeleted
	}
}

func New(cfg *config.Config, s *storage.Storage, q *queue.Queue, templatesFS, staticFS fs.FS, opts ...ServerOption) (*Server, error) {
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
	tmplSettings, err := template.New("").Funcs(funcMap).ParseFS(templatesFS, "templates/layout.html", "templates/settings.html")
	if err != nil {
		return nil, err
	}

	srv := &Server{
		cfg:          cfg,
		storage:      s,
		queue:        q,
		tmplIndex:    tmplIndex,
		tmplRepo:     tmplRepo,
		tmplDrift:    tmplDrift,
		tmplSettings: tmplSettings,
		staticFS:     staticFS,
		rateLimiters: make(map[string]*rateLimiterEntry),
	}

	for _, opt := range opts {
		opt(srv)
	}

	metrics.Register(q)

	return srv, nil
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Get("/metrics", promhttp.Handler().ServeHTTP)

	r.Group(func(r chi.Router) {
		if s.cfg.UIAuth.Username != "" || s.cfg.UIAuth.Password != "" {
			r.Use(s.uiAuthMiddleware)
		}
		r.Use(s.csrfMiddleware)
		r.Get("/", s.handleIndex)
		r.Get("/repos/{repo}", s.handleRepo)
		r.Post("/repos/{repo}/scan", s.handleScanRepoUI)
		r.Get("/repos/{repo}/stacks/*", s.handleStack)
		r.Post("/repos/{repo}/stacks/*", s.handleScanStackUI)
		r.Get("/settings", s.handleSettings)
		r.Get("/settings/repos", s.handleSettings)
	})

	r.Route("/api", func(r chi.Router) {
		if s.apiAuthEnabled() {
			r.Use(s.apiAuthMiddleware)
		}
		r.Get("/health", s.handleHealth)
		r.Get("/stacks/{stackID}", s.handleGetStackScan)
		r.Get("/scans/{scanID}", s.handleGetScan)
		r.Get("/repos/{repo}/stacks", s.handleListRepoStackScans)
		r.Get("/repos/{repo}/events", s.handleRepoEvents)
		r.Get("/events", s.handleGlobalEvents)
		r.With(s.rateLimitMiddleware).Post("/repos/{repo}/scan", s.handleScanRepo)
		r.With(s.rateLimitMiddleware).Post("/repos/{repo}/stacks/*", s.handleScanStack)
		if s.cfg.Webhook.Enabled {
			r.Post("/webhooks/github", s.handleGitHubWebhook)
		}

		r.Route("/settings", func(r chi.Router) {
			r.Use(s.settingsAuthMiddleware)
			r.Get("/integrations", s.handleListSettingsIntegrations)
			r.Post("/integrations", s.handleCreateSettingsIntegration)
			r.Get("/integrations/{integration}", s.handleGetSettingsIntegration)
			r.Put("/integrations/{integration}", s.handleUpdateSettingsIntegration)
			r.Delete("/integrations/{integration}", s.handleDeleteSettingsIntegration)
			r.Get("/repos", s.handleListSettingsRepos)
			r.Post("/repos", s.handleCreateSettingsRepo)
			r.Get("/repos/{repo}", s.handleGetSettingsRepo)
			r.Put("/repos/{repo}", s.handleUpdateSettingsRepo)
			r.Delete("/repos/{repo}", s.handleDeleteSettingsRepo)
			r.Post("/repos/{repo}/test", s.handleTestRepoConnection)
		})
	})

	staticHandler, _ := fs.Sub(s.staticFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticHandler))))

	return r
}
