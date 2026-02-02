package api

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/storage"
	"github.com/go-chi/chi/v5"
)

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

type stackPageData struct {
	RepoName  string
	RepoURL   string
	Path      string
	Result    *storage.RunResult
	Scan      *queue.Scan
	CSRFToken string
	PlanHTML  template.HTML
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
