package api

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/driftdhq/driftd/internal/storage"
	"github.com/go-chi/chi/v5"
)

type indexData struct {
	Repos         []repoStatusData
	ConfigRepos   []config.RepoConfig
	RepoByName    map[string]repoStatusData
	ConfigByName  map[string]config.RepoConfig
	TotalStacks   int
	HealthyStacks int
	HealthyPct    int
	DriftedStacks int
	ErrorStacks   int
	ActiveScans   int
}

type repoStatusData struct {
	Name          string
	Drifted       bool
	Stacks        int
	DriftedStacks int
	ErrorStacks   int
	HealthyStacks int
	Locked        bool
	LastRun       time.Time
	CommitSHA     string
	Active        bool
	Progress      string
}

type repoPageData struct {
	Name       string
	Stacks     []storage.StackStatus
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
		errorStacks := 0
		if stacks, err := s.storage.ListStacks(repo.Name); err == nil {
			for _, stack := range stacks {
				if stack.Error != "" {
					errorStacks++
				}
			}
		}
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
		healthyStacks := repo.Stacks - repo.DriftedStacks - errorStacks
		if healthyStacks < 0 {
			healthyStacks = 0
		}
		repoData = append(repoData, repoStatusData{
			Name:          repo.Name,
			Drifted:       repo.Drifted,
			Stacks:        repo.Stacks,
			DriftedStacks: repo.DriftedStacks,
			ErrorStacks:   errorStacks,
			HealthyStacks: healthyStacks,
			Locked:        locked,
			LastRun:       lastRun,
			CommitSHA:     commit,
			Active:        active,
			Progress:      progress,
		})
	}

	totalStacks := 0
	driftedStacks := 0
	errorStacks := 0
	activeScans := 0
	for _, repo := range repoData {
		totalStacks += repo.Stacks
		driftedStacks += repo.DriftedStacks
		errorStacks += repo.ErrorStacks
		if repo.Active {
			activeScans++
		}
	}
	healthyStacks := totalStacks - driftedStacks - errorStacks
	if healthyStacks < 0 {
		healthyStacks = 0
	}
	healthyPct := 0
	if totalStacks > 0 {
		healthyPct = (healthyStacks * 100) / totalStacks
	}

	configRepos := s.listConfiguredRepos()
	data := indexData{
		Repos:         repoData,
		ConfigRepos:   configRepos,
		RepoByName:    map[string]repoStatusData{},
		ConfigByName:  map[string]config.RepoConfig{},
		TotalStacks:   totalStacks,
		HealthyStacks: healthyStacks,
		HealthyPct:    healthyPct,
		DriftedStacks: driftedStacks,
		ErrorStacks:   errorStacks,
		ActiveScans:   activeScans,
	}
	for _, repo := range repoData {
		data.RepoByName[repo.Name] = repo
	}
	for _, repo := range configRepos {
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
	stacks = filterParentStackStatuses(stacks)
	csrfToken := csrfTokenFromContext(r.Context())
	repoCfg, _ := s.getRepoConfig(repoName)
	locked, _ := s.queue.IsRepoLocked(r.Context(), repoName)
	activeScan, _ := s.queue.GetActiveScan(r.Context(), repoName)
	lastScan, _ := s.queue.GetLastScan(r.Context(), repoName)

	data := repoPageData{
		Name:       repoName,
		Stacks:     stacks,
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

func (s *Server) handleScanStackUI(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	stackPath := chi.URLParam(r, "*")
	if !isValidRepoName(repoName) || !isSafeStackPath(stackPath) {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	repoCfg, err := s.getRepoConfig(repoName)
	if err != nil || repoCfg == nil {
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
		Trigger:    trigger,
	}

	if err := s.queue.Enqueue(r.Context(), stackScan); err != nil {
		_ = s.queue.MarkScanEnqueueFailed(r.Context(), scan.ID)
		http.Error(w, s.sanitizeErrorMessage(err.Error()), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/repos/"+repoName, http.StatusSeeOther)
}

func filterParentStackStatuses(stacks []storage.StackStatus) []storage.StackStatus {
	if len(stacks) < 2 {
		return stacks
	}
	sort.Slice(stacks, func(i, j int) bool {
		return stacks[i].Path < stacks[j].Path
	})
	filtered := make([]storage.StackStatus, 0, len(stacks))
	for i, stack := range stacks {
		prefix := stack.Path
		if prefix != "" {
			prefix += "/"
		}
		hasChild := false
		for j := i + 1; j < len(stacks); j++ {
			if strings.HasPrefix(stacks[j].Path, prefix) {
				hasChild = true
				break
			}
		}
		if !hasChild {
			filtered = append(filtered, stack)
		}
	}
	return filtered
}

func (s *Server) handleStack(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	stackPath := chi.URLParam(r, "*")
	if !isValidRepoName(repoName) || !isSafeStackPath(stackPath) {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	repoCfg, _ := s.getRepoConfig(repoName)
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

type settingsData struct {
	CSRFToken                  string
	DynamicReposEnabled        bool
	DynamicIntegrationsEnabled bool
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	data := settingsData{
		CSRFToken:                  csrfTokenFromContext(r.Context()),
		DynamicReposEnabled:        s.repoStore != nil,
		DynamicIntegrationsEnabled: s.intStore != nil,
	}

	if err := s.tmplSettings.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("template error: %v", err)
	}
}
