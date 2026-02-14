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
	// Route uses wildcard due to slashes in IDs.
	stackID := chi.URLParam(r, "*")

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
	json.NewEncoder(w).Encode(toAPIStackScan(stackScan))
}

func (s *Server) handleListProjectStackScans(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !isValidProjectName(projectName) {
		http.Error(w, "Invalid project name", http.StatusBadRequest)
		return
	}

	stackScans, err := s.queue.ListProjectStackScans(r.Context(), projectName, 50)
	if err != nil {
		http.Error(w, s.sanitizeErrorMessage(err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	apiScans := make([]*apiStackScan, 0, len(stackScans))
	for _, scan := range stackScans {
		apiScans = append(apiScans, toAPIStackScan(scan))
	}
	json.NewEncoder(w).Encode(apiScans)
}

type scanRequest struct {
	Trigger string `json:"trigger,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Actor   string `json:"actor,omitempty"`
}

func normalizeScanTrigger(trigger string) string {
	trigger = strings.TrimSpace(trigger)
	if trigger == "" {
		return "manual"
	}
	return trigger
}

type scanResponse struct {
	Stacks     []string   `json:"stacks,omitempty"`
	Scan       *apiScan   `json:"scan,omitempty"`
	Scans      []*apiScan `json:"scans,omitempty"`
	ActiveScan *apiScan   `json:"active_scan,omitempty"`
	Message    string     `json:"message,omitempty"`
	Error      string     `json:"error,omitempty"`
}

func (s *Server) handleScanProjectUI(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !isValidProjectName(projectName) {
		http.Error(w, "Invalid project name", http.StatusBadRequest)
		return
	}

	projectCfg, err := s.getProjectConfig(projectName)
	if err != nil || projectCfg == nil {
		http.Error(w, "Project not configured", http.StatusNotFound)
		return
	}

	trigger := "manual"
	_, enqResult, err := s.orchestrator.StartAndEnqueue(r.Context(), projectCfg, trigger, "", "")
	if err != nil {
		if err == queue.ErrProjectLocked {
			http.Redirect(w, r, "/projects/"+projectName, http.StatusSeeOther)
			return
		}
		if err == orchestrate.ErrNoStacksEnqueued {
			http.Redirect(w, r, "/projects/"+projectName, http.StatusSeeOther)
			return
		}
		http.Error(w, s.sanitizeErrorMessage(err.Error()), http.StatusInternalServerError)
		return
	}

	if err == orchestrate.ErrNoStacksEnqueued || (enqResult != nil && len(enqResult.StackIDs) == 0) {
		http.Redirect(w, r, "/projects/"+projectName, http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/projects/"+projectName, http.StatusSeeOther)
}

func (s *Server) handleScanRepo(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !isValidProjectName(projectName) {
		http.Error(w, "Invalid project name", http.StatusBadRequest)
		return
	}

	projectCfg, err := s.getProjectConfig(projectName)
	if err != nil || projectCfg == nil {
		http.Error(w, "Project not configured", http.StatusNotFound)
		return
	}

	var req scanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	trigger := normalizeScanTrigger(req.Trigger)
	scan, enqResult, err := s.orchestrator.StartAndEnqueue(r.Context(), projectCfg, trigger, req.Commit, req.Actor)
	if err != nil {
		if err == queue.ErrProjectLocked {
			activeScan, activeErr := s.queue.GetActiveScan(r.Context(), projectName)
			if activeErr != nil {
				http.Error(w, "Project scan already in progress", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(scanResponse{
				Error:      "Project scan already in progress",
				ActiveScan: toAPIScan(activeScan),
			})
			return
		}
		if err == orchestrate.ErrNoStacksEnqueued {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(scanResponse{
				Error: "No stacks enqueued (all inflight)",
			})
			return
		}
		http.Error(w, s.sanitizeErrorMessage(err.Error()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	resp := scanResponse{
		Stacks:  enqResult.StackIDs,
		Scan:    toAPIScan(scan),
		Message: fmt.Sprintf("Enqueued %d stacks", len(enqResult.StackIDs)),
	}
	if len(enqResult.Errors) > 0 {
		resp.Error = strings.Join(enqResult.Errors, "; ")
	}

	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleScanStack(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	stackPath := chi.URLParam(r, "*")
	if !isValidProjectName(projectName) || !pathutil.IsSafeStackPath(stackPath) {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	projectCfg, err := s.getProjectConfig(projectName)
	if err != nil || projectCfg == nil {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(scanResponse{Error: "Project not configured"})
		return
	}

	var req scanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	trigger := normalizeScanTrigger(req.Trigger)
	scan, stacks, err := s.startScanWithCancel(r.Context(), projectCfg, trigger, req.Commit, req.Actor)
	if err != nil {
		if err == queue.ErrProjectLocked {
			activeScan, activeErr := s.queue.GetActiveScan(r.Context(), projectName)
			if activeErr != nil {
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(scanResponse{Error: "Project scan already in progress"})
				return
			}
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(scanResponse{Error: "Project scan already in progress", ActiveScan: toAPIScan(activeScan)})
			return
		}
		http.Error(w, s.sanitizeErrorMessage(err.Error()), http.StatusInternalServerError)
		return
	}
	// startScanWithCancel handles lock renewal and version detection

	if !containsStack(stackPath, stacks) {
		_ = s.queue.FailScan(r.Context(), scan.ID, projectName, "stack not found")
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}

	enqResult, enqueueErr := s.orchestrator.EnqueueStacks(r.Context(), scan, projectCfg, []string{stackPath}, trigger, req.Commit, req.Actor)
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
		Scan:    toAPIScan(scan),
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
	json.NewEncoder(w).Encode(toAPIScan(scan))
}

func (s *Server) handleProjectEvents(w http.ResponseWriter, r *http.Request) {
	projectName := chi.URLParam(r, "project")
	if !isValidProjectName(projectName) {
		http.Error(w, "Invalid project name", http.StatusBadRequest)
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

	activeScan, _ := s.queue.GetActiveScan(r.Context(), projectName)
	lastScan, _ := s.queue.GetLastScan(r.Context(), projectName)
	stacks, _ := s.storage.ListStacks(projectName)
	payload, _ := buildSnapshotPayload(projectName, activeScan, lastScan, stacks)
	fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", payload)
	flusher.Flush()

	sub := s.queue.Client().Subscribe(r.Context(), "driftd:events:"+projectName)
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
			var event queue.ProjectEvent
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

	// Emit an initial SSE comment so headers are flushed and clients can
	// establish the stream before the first project event is published.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

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
			var event queue.ProjectEvent
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
