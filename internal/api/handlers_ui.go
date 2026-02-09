package api

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/pathutil"
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
	Pagination repoPagination
	Sort       string
	Order      string
}

type repoPagination struct {
	Page       int
	PerPage    int
	Total      int
	TotalPages int
	PrevURL    string
	NextURL    string
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
	page, perPage, sortBy, sortOrder := parseRepoListParams(r)
	stacks = sortStacks(stacks, sortBy, sortOrder)
	pageStacks, pagination := paginateStacks(stacks, page, perPage, "/repos/"+repoName, sortBy, sortOrder)
	csrfToken := csrfTokenFromContext(r.Context())
	repoCfg, _ := s.getRepoConfig(repoName)
	locked, _ := s.queue.IsRepoLocked(r.Context(), repoName)
	activeScan, _ := s.queue.GetActiveScan(r.Context(), repoName)
	lastScan, _ := s.queue.GetLastScan(r.Context(), repoName)

	data := repoPageData{
		Name:       repoName,
		Stacks:     pageStacks,
		Config:     repoCfg,
		Locked:     locked,
		ActiveScan: activeScan,
		LastScan:   lastScan,
		CSRFToken:  csrfToken,
		Pagination: pagination,
		Sort:       sortBy,
		Order:      sortOrder,
	}

	if err := s.tmplRepo.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("template error: %v", err)
	}
}

func parseRepoListParams(r *http.Request) (page, perPage int, sortBy, sortOrder string) {
	q := r.URL.Query()
	page = clampInt(parseInt(q.Get("page"), 1), 1, 10_000)
	perPage = clampInt(parseInt(q.Get("per"), 50), 10, 200)
	sortBy = q.Get("sort")
	if sortBy == "" {
		sortBy = "path"
	}
	switch sortBy {
	case "path", "status", "last_run":
	default:
		sortBy = "path"
	}
	sortOrder = strings.ToLower(q.Get("order"))
	if sortOrder != "asc" && sortOrder != "desc" {
		sortOrder = "asc"
	}
	return page, perPage, sortBy, sortOrder
}

func parseInt(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func sortStacks(stacks []storage.StackStatus, sortBy, sortOrder string) []storage.StackStatus {
	if len(stacks) < 2 {
		return stacks
	}
	sorted := make([]storage.StackStatus, len(stacks))
	copy(sorted, stacks)
	less := func(i, j int) bool {
		switch sortBy {
		case "status":
			// error -> drifted -> healthy
			ai := statusRank(sorted[i])
			aj := statusRank(sorted[j])
			if ai != aj {
				return ai < aj
			}
		case "last_run":
			ti := sorted[i].RunAt
			tj := sorted[j].RunAt
			if !ti.Equal(tj) {
				return ti.Before(tj)
			}
		}
		return sorted[i].Path < sorted[j].Path
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sortOrder == "desc" {
			return !less(i, j)
		}
		return less(i, j)
	})
	return sorted
}

func statusRank(stack storage.StackStatus) int {
	if stack.Error != "" {
		return 0
	}
	if stack.Drifted {
		return 1
	}
	return 2
}

func paginateStacks(stacks []storage.StackStatus, page, perPage int, basePath, sortBy, sortOrder string) ([]storage.StackStatus, repoPagination) {
	total := len(stacks)
	totalPages := total / perPage
	if total%perPage != 0 {
		totalPages++
	}
	if totalPages == 0 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}

	start := (page - 1) * perPage
	if start > total {
		start = total
	}
	end := start + perPage
	if end > total {
		end = total
	}

	pagination := repoPagination{
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: totalPages,
	}
	if page > 1 {
		pagination.PrevURL = buildRepoListURL(basePath, page-1, perPage, sortBy, sortOrder)
	}
	if page < totalPages {
		pagination.NextURL = buildRepoListURL(basePath, page+1, perPage, sortBy, sortOrder)
	}
	return stacks[start:end], pagination
}

func buildRepoListURL(basePath string, page, perPage int, sortBy, sortOrder string) string {
	params := url.Values{}
	params.Set("page", strconv.Itoa(page))
	params.Set("per", strconv.Itoa(perPage))
	params.Set("sort", sortBy)
	params.Set("order", sortOrder)
	return basePath + "?" + params.Encode()
}

func (s *Server) handleScanStackUI(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	stackPath := chi.URLParam(r, "*")
	if !isValidRepoName(repoName) || !pathutil.IsSafeStackPath(stackPath) {
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
	if !isValidRepoName(repoName) || !pathutil.IsSafeStackPath(stackPath) {
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
