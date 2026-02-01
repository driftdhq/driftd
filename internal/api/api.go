package api

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/cbrown132/driftd/internal/config"
	"github.com/cbrown132/driftd/internal/queue"
	"github.com/cbrown132/driftd/internal/storage"
	"github.com/cbrown132/driftd/internal/version"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type Server struct {
	cfg      *config.Config
	storage  *storage.Storage
	queue    *queue.Queue
	tmpl     *template.Template
	staticFS fs.FS
}

func New(cfg *config.Config, s *storage.Storage, q *queue.Queue, templatesFS, staticFS fs.FS) (*Server, error) {
	funcMap := template.FuncMap{
		"timeAgo": timeAgo,
	}

	tmpl, err := template.New("").Funcs(funcMap).ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}

	return &Server{
		cfg:      cfg,
		storage:  s,
		queue:    q,
		tmpl:     tmpl,
		staticFS: staticFS,
	}, nil
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// HTML routes
	r.Get("/", s.handleIndex)
	r.Get("/repos/{repo}", s.handleRepo)
	r.Get("/repos/{repo}/stacks/*", s.handleStack)

	// API routes
	r.Route("/api", func(r chi.Router) {
		r.Get("/health", s.handleHealth)
		r.Get("/jobs/{jobID}", s.handleGetJob)
		r.Get("/tasks/{taskID}", s.handleGetTask)
		r.Get("/repos/{repo}/jobs", s.handleListRepoJobs)
		r.Post("/repos/{repo}/scan", s.handleScanRepo)
		r.Post("/repos/{repo}/stacks/{stack:.*}/scan", s.handleScanStack)
	})

	// Static files from embedded FS
	staticHandler, _ := fs.Sub(s.staticFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticHandler))))

	return r
}

type indexData struct {
	Repos       []repoStatusData
	ConfigRepos []config.RepoConfig
}

type repoStatusData struct {
	Name    string
	Drifted bool
	Stacks  int
	Locked  bool
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	repos, _ := s.storage.ListRepos()

	var repoData []repoStatusData
	for _, repo := range repos {
		locked, _ := s.queue.IsRepoLocked(r.Context(), repo.Name)
		repoData = append(repoData, repoStatusData{
			Name:    repo.Name,
			Drifted: repo.Drifted,
			Stacks:  repo.Stacks,
			Locked:  locked,
		})
	}

	data := indexData{
		Repos:       repoData,
		ConfigRepos: s.cfg.Repos,
	}

	if err := s.tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		log.Printf("template error: %v", err)
	}
}

type repoPageData struct {
	Name   string
	Stacks []storage.StackStatus
	Config *config.RepoConfig
	Locked bool
}

func (s *Server) handleRepo(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")

	stacks, _ := s.storage.ListStacks(repoName)
	repoCfg := s.cfg.GetRepo(repoName)
	locked, _ := s.queue.IsRepoLocked(r.Context(), repoName)

	data := repoPageData{
		Name:   repoName,
		Stacks: stacks,
		Config: repoCfg,
		Locked: locked,
	}

	if err := s.tmpl.ExecuteTemplate(w, "repo.html", data); err != nil {
		log.Printf("template error: %v", err)
	}
}

type stackPageData struct {
	RepoName string
	Path     string
	Result   *storage.RunResult
}

func (s *Server) handleStack(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	stackPath := chi.URLParam(r, "*")

	result, err := s.storage.GetResult(repoName, stackPath)
	if err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}

	data := stackPageData{
		RepoName: repoName,
		Path:     stackPath,
		Result:   result,
	}

	if err := s.tmpl.ExecuteTemplate(w, "drift.html", data); err != nil {
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

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")

	job, err := s.queue.GetJob(r.Context(), jobID)
	if err != nil {
		if err == queue.ErrJobNotFound {
			http.Error(w, "Job not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(job)
}

func (s *Server) handleListRepoJobs(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")

	jobs, err := s.queue.ListRepoJobs(r.Context(), repoName, 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jobs)
}

type scanRequest struct {
	Trigger string `json:"trigger,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Actor   string `json:"actor,omitempty"`
}

type scanResponse struct {
	Jobs       []string    `json:"jobs,omitempty"`
	Task       *queue.Task `json:"task,omitempty"`
	ActiveTask *queue.Task `json:"active_task,omitempty"`
	Message    string      `json:"message,omitempty"`
	Error      string      `json:"error,omitempty"`
}

func (s *Server) handleScanRepo(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")

	repoCfg := s.cfg.GetRepo(repoName)
	if repoCfg == nil {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(scanResponse{Error: "Repository not configured"})
		return
	}

	var req scanRequest
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req)
	}
	if req.Trigger == "" {
		req.Trigger = "manual"
	}

	maxRetries := 0
	if s.cfg.Worker.RetryOnce {
		maxRetries = 1
	}

	task, err := s.queue.StartTask(r.Context(), repoName, req.Trigger, req.Commit, req.Actor, len(repoCfg.Stacks))
	if err != nil {
		if err == queue.ErrRepoLocked {
			activeTask, activeErr := s.queue.GetActiveTask(r.Context(), repoName)
			if activeErr != nil {
				http.Error(w, "Repository scan already in progress", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(scanResponse{
				Error:      "Repository scan already in progress",
				ActiveTask: activeTask,
			})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go s.queue.RenewTaskLock(context.Background(), task.ID, repoName, s.cfg.Worker.TaskMaxAge, s.cfg.Worker.RenewEvery)

	versions, err := version.DetectFromRepoURL(r.Context(), repoCfg.URL, repoCfg.Stacks)
	if err != nil {
		_ = s.queue.FailTask(r.Context(), task.ID, repoName, err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = s.queue.SetTaskVersions(r.Context(), task.ID, versions.DefaultTerraform, versions.DefaultTerragrunt, versions.StackTerraform, versions.StackTerragrunt)

	var jobIDs []string
	var errors []string

	for _, stackPath := range repoCfg.Stacks {
		job := &queue.Job{
			TaskID:     task.ID,
			RepoName:   repoName,
			RepoURL:    repoCfg.URL,
			StackPath:  stackPath,
			MaxRetries: maxRetries,
			Trigger:    req.Trigger,
			Commit:     req.Commit,
			Actor:      req.Actor,
		}

		if err := s.queue.Enqueue(r.Context(), job); err != nil {
			_ = s.queue.MarkTaskEnqueueFailed(r.Context(), task.ID)
			if err == queue.ErrRepoLocked {
				errors = append(errors, fmt.Sprintf("%s: repo locked", stackPath))
			} else {
				errors = append(errors, fmt.Sprintf("%s: %v", stackPath, err))
			}
			continue
		}
		jobIDs = append(jobIDs, job.ID)
	}

	w.Header().Set("Content-Type", "application/json")

	if len(jobIDs) == 0 && len(errors) > 0 {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(scanResponse{
			Error:   "Failed to enqueue any jobs",
			Message: strings.Join(errors, "; "),
		})
		return
	}

	resp := scanResponse{
		Jobs:    jobIDs,
		Task:    task,
		Message: fmt.Sprintf("Enqueued %d jobs", len(jobIDs)),
	}
	if len(errors) > 0 {
		resp.Error = strings.Join(errors, "; ")
	}

	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleScanStack(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	stackPath := chi.URLParam(r, "stack")

	repoCfg := s.cfg.GetRepo(repoName)
	if repoCfg == nil {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(scanResponse{Error: "Repository not configured"})
		return
	}

	var req scanRequest
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req)
	}
	if req.Trigger == "" {
		req.Trigger = "manual"
	}

	maxRetries := 0
	if s.cfg.Worker.RetryOnce {
		maxRetries = 1
	}

	task, err := s.queue.StartTask(r.Context(), repoName, req.Trigger, req.Commit, req.Actor, 1)
	if err != nil {
		if err == queue.ErrRepoLocked {
			activeTask, activeErr := s.queue.GetActiveTask(r.Context(), repoName)
			if activeErr != nil {
				http.Error(w, "Repository scan already in progress", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(scanResponse{
				Error:      "Repository scan already in progress",
				ActiveTask: activeTask,
			})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go s.queue.RenewTaskLock(context.Background(), task.ID, repoName, s.cfg.Worker.TaskMaxAge, s.cfg.Worker.RenewEvery)

	versions, err := version.DetectFromRepoURL(r.Context(), repoCfg.URL, []string{stackPath})
	if err != nil {
		_ = s.queue.FailTask(r.Context(), task.ID, repoName, err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = s.queue.SetTaskVersions(r.Context(), task.ID, versions.DefaultTerraform, versions.DefaultTerragrunt, versions.StackTerraform, versions.StackTerragrunt)

	job := &queue.Job{
		TaskID:     task.ID,
		RepoName:   repoName,
		RepoURL:    repoCfg.URL,
		StackPath:  stackPath,
		MaxRetries: maxRetries,
		Trigger:    req.Trigger,
		Commit:     req.Commit,
		Actor:      req.Actor,
	}

	if err := s.queue.Enqueue(r.Context(), job); err != nil {
		_ = s.queue.MarkTaskEnqueueFailed(r.Context(), task.ID)
		w.Header().Set("Content-Type", "application/json")
		if err == queue.ErrRepoLocked {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(scanResponse{Error: "Repository scan already in progress"})
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(scanResponse{Error: err.Error()})
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(scanResponse{
		Jobs:    []string{job.ID},
		Task:    task,
		Message: "Job enqueued",
	})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")

	task, err := s.queue.GetTask(r.Context(), taskID)
	if err != nil {
		if err == queue.ErrTaskNotFound {
			http.Error(w, "Task not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(task)
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
