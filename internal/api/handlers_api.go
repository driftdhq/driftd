package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/driftdhq/driftd/internal/orchestrate"
	"github.com/driftdhq/driftd/internal/pathutil"
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/go-chi/chi/v5"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
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

	repoCfg, err := s.getRepoConfig(repoName)
	if err != nil || repoCfg == nil {
		http.Error(w, "Repository not configured", http.StatusNotFound)
		return
	}

	trigger := "manual"
	scan, stacks, err := s.startScanWithCancel(r.Context(), repoCfg, trigger, "", "")
	if err != nil {
		if err == queue.ErrRepoLocked {
			http.Redirect(w, r, "/repos/"+repoName, http.StatusSeeOther)
			return
		}
		http.Error(w, s.sanitizeErrorMessage(err.Error()), http.StatusInternalServerError)
		return
	}

	if _, err := s.orchestrator.EnqueueStacks(r.Context(), scan, repoCfg, stacks, trigger, "", ""); err != nil {
		if err == orchestrate.ErrNoStacksEnqueued {
			http.Redirect(w, r, "/repos/"+repoName, http.StatusSeeOther)
			return
		}
		http.Error(w, s.sanitizeErrorMessage(err.Error()), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/repos/"+repoName, http.StatusSeeOther)
}

func (s *Server) handleScanRepo(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	if !isValidRepoName(repoName) {
		http.Error(w, "Invalid repository name", http.StatusBadRequest)
		return
	}

	repoCfg, err := s.getRepoConfig(repoName)
	if err != nil || repoCfg == nil {
		http.Error(w, "Repository not configured", http.StatusNotFound)
		return
	}

	var req scanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
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

	enqResult, enqueueErr := s.orchestrator.EnqueueStacks(r.Context(), scan, repoCfg, stacks, req.Trigger, req.Commit, req.Actor)

	w.Header().Set("Content-Type", "application/json")

	if enqueueErr != nil {
		if enqueueErr == orchestrate.ErrNoStacksEnqueued {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(scanResponse{
				Error:   "No stacks enqueued (all inflight)",
				Message: strings.Join(enqResult.Errors, "; "),
			})
			return
		}
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(scanResponse{
			Error:   "Failed to enqueue any stacks",
			Message: enqueueErr.Error(),
		})
		return
	}

	resp := scanResponse{
		Stacks:  enqResult.StackIDs,
		Scan:    scan,
		Message: fmt.Sprintf("Enqueued %d stacks", len(enqResult.StackIDs)),
	}
	if len(enqResult.Errors) > 0 {
		resp.Error = strings.Join(enqResult.Errors, "; ")
	}

	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleScanStack(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	stackPath := chi.URLParam(r, "*")
	if !isValidRepoName(repoName) || !pathutil.IsSafeStackPath(stackPath) {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	repoCfg, err := s.getRepoConfig(repoName)
	if err != nil || repoCfg == nil {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(scanResponse{Error: "Repository not configured"})
		return
	}

	var req scanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	scan, stacks, err := s.startScanWithCancel(r.Context(), repoCfg, req.Trigger, req.Commit, req.Actor)
	if err != nil {
		if err == queue.ErrRepoLocked {
			activeScan, activeErr := s.queue.GetActiveScan(r.Context(), repoName)
			if activeErr != nil {
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(scanResponse{Error: "Repository scan already in progress"})
				return
			}
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(scanResponse{Error: "Repository scan already in progress", ActiveScan: activeScan})
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

	enqResult, enqueueErr := s.orchestrator.EnqueueStacks(r.Context(), scan, repoCfg, []string{stackPath}, req.Trigger, req.Commit, req.Actor)
	w.Header().Set("Content-Type", "application/json")
	if enqueueErr != nil {
		if enqueueErr == orchestrate.ErrNoStacksEnqueued {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(scanResponse{Error: "No stacks enqueued (all inflight)"})
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(scanResponse{Error: s.sanitizeErrorMessage(enqueueErr.Error())})
		}
		return
	}

	json.NewEncoder(w).Encode(scanResponse{
		Stacks:  enqResult.StackIDs,
		Scan:    scan,
		Message: "Stack enqueued",
	})
}

func (s *Server) handleGetScan(w http.ResponseWriter, r *http.Request) {
	scanID := chi.URLParam(r, "scanID")
	if scanID == "" {
		http.Error(w, "Missing scan ID", http.StatusBadRequest)
		return
	}

	scan, err := s.queue.GetScan(r.Context(), scanID)
	if err != nil {
		if err == queue.ErrScanNotFound {
			http.Error(w, "Scan not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to get scan", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(scan)
}

func (s *Server) handleRepoEvents(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	if !isValidRepoName(repoName) {
		http.Error(w, "Invalid repository name", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	activeScan, _ := s.queue.GetActiveScan(r.Context(), repoName)
	lastScan, _ := s.queue.GetLastScan(r.Context(), repoName)
	stacks, _ := s.storage.ListStacks(repoName)
	payload, _ := buildSnapshotPayload(repoName, activeScan, lastScan, stacks)
	fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", payload)
	flusher.Flush()

	sub := s.queue.Client().Subscribe(r.Context(), "driftd:events:"+repoName)
	defer sub.Close()

	ch := sub.Channel()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var event queue.RepoEvent
			if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
				continue
			}
			updatePayload, err := buildUpdatePayload(&event)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: update\ndata: %s\n\n", updatePayload)
			flusher.Flush()
		}
	}
}

func (s *Server) handleGlobalEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	sub := s.queue.Client().PSubscribe(r.Context(), "driftd:events:*")
	defer sub.Close()

	ch := sub.Channel()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var event queue.RepoEvent
			if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
				continue
			}
			updatePayload, err := buildUpdatePayload(&event)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: update\ndata: %s\n\n", updatePayload)
			flusher.Flush()
		}
	}
}
