package api

import (
	"html/template"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/metrics"
	"github.com/driftdhq/driftd/internal/orchestrate"
	"github.com/driftdhq/driftd/internal/projects"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/secrets"
	"github.com/driftdhq/driftd/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"
)

type Server struct {
	cfg             *config.Config
	storage         storage.Store
	queue           *queue.Queue
	projectStore    *secrets.ProjectStore
	intStore        *secrets.IntegrationStore
	projectProvider projects.Provider
	orchestrator    *orchestrate.ScanOrchestrator
	tmplIndex       *template.Template
	tmplRepo        *template.Template
	tmplDrift       *template.Template
	tmplSettings    *template.Template
	staticFS        fs.FS

	rateLimitMu  sync.Mutex
	rateLimiters map[string]*rateLimiterEntry

	onProjectAdded   func(name, schedule string)
	onProjectUpdated func(name, schedule string)
	onProjectDeleted func(name string)
}

type rateLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// ServerOption is a functional option for configuring the Server.
type ServerOption func(*Server)

// WithProjectStore sets the dynamic repository store.
func WithProjectStore(rs *secrets.ProjectStore) ServerOption {
	return func(s *Server) {
		s.projectStore = rs
	}
}

// WithIntegrationStore sets the integration store.
func WithIntegrationStore(is *secrets.IntegrationStore) ServerOption {
	return func(s *Server) {
		s.intStore = is
	}
}

// WithProjectProvider sets a repository provider for resolving dynamic projects.
func WithProjectProvider(provider projects.Provider) ServerOption {
	return func(s *Server) {
		s.projectProvider = provider
	}
}

// WithSchedulerCallbacks sets callbacks for scheduler integration when projects change.
func WithSchedulerCallbacks(onAdded, onUpdated func(name, schedule string), onDeleted func(name string)) ServerOption {
	return func(s *Server) {
		s.onProjectAdded = onAdded
		s.onProjectUpdated = onUpdated
		s.onProjectDeleted = onDeleted
	}
}

// WithOrchestrator sets a shared scan orchestrator.
func WithOrchestrator(orch *orchestrate.ScanOrchestrator) ServerOption {
	return func(s *Server) {
		s.orchestrator = orch
	}
}

func New(cfg *config.Config, s storage.Store, q *queue.Queue, templatesFS, staticFS fs.FS, opts ...ServerOption) (*Server, error) {
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
	tmplRepo, err := template.New("").Funcs(funcMap).ParseFS(templatesFS, "templates/layout.html", "templates/project.html")
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

	if srv.orchestrator == nil {
		srv.orchestrator = orchestrate.New(cfg, q)
	}
	metrics.Register(q)

	return srv, nil
}

// Stop gracefully shuts down background goroutines (e.g. lock renewals).
func (s *Server) Stop() {
	s.orchestrator.Stop()
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
		r.Get("/projects/{project}", s.handleRepo)
		r.Post("/projects/{project}/scan", s.handleScanProjectUI)
		r.Get("/projects/{project}/stacks/*", s.handleStack)
		r.Post("/projects/{project}/stacks/*", s.handleScanStackUI)
		r.Get("/settings", s.handleSettings)
		r.Get("/settings/projects", s.handleSettings)
	})

	// SSE endpoints use UI auth (cookie/basic-auth) since EventSource
	// doesn't support custom headers required by API token auth.
	r.Group(func(r chi.Router) {
		if s.cfg.UIAuth.Username != "" || s.cfg.UIAuth.Password != "" {
			r.Use(s.uiAuthMiddleware)
		}
		r.Get("/api/projects/{project}/events", s.handleProjectEvents)
		r.Get("/api/events", s.handleGlobalEvents)
	})

	r.Route("/api", func(r chi.Router) {
		if s.apiAuthEnabled() {
			r.Use(s.apiAuthMiddleware)
		}
		r.Get("/health", s.handleHealth)
		// Stack scan IDs can contain slashes (stack paths), so use a wildcard.
		r.Get("/stacks/*", s.handleGetStackScan)
		r.Get("/scans/{scanID}", s.handleGetScan)
		r.Get("/projects/{project}/stacks", s.handleListProjectStackScans)
		r.With(s.rateLimitMiddleware).Post("/projects/{project}/scan", s.handleScanRepo)
		r.With(s.rateLimitMiddleware).Post("/projects/{project}/stacks/*", s.handleScanStack)
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
			r.Get("/projects", s.handleListSettingsRepos)
			r.Post("/projects", s.handleCreateSettingsRepo)
			r.Get("/projects/{project}", s.handleGetSettingsRepo)
			r.Put("/projects/{project}", s.handleUpdateSettingsRepo)
			r.Delete("/projects/{project}", s.handleDeleteSettingsRepo)
			r.Post("/projects/{project}/test", s.handleTestProjectConnection)
		})
	})

	staticHandler, _ := fs.Sub(s.staticFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticHandler))))

	return r
}
