package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/driftdhq/driftd/internal/pathutil"
	"github.com/driftdhq/driftd/internal/storage"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

type Runner struct {
	storage *storage.Storage
}

func New(s *storage.Storage) *Runner {
	return &Runner{storage: s}
}

type RunResult struct {
	Drifted    bool
	Added      int
	Changed    int
	Destroyed  int
	PlanOutput string
	Error      string
	RunAt      time.Time
}

func (r *Runner) Run(ctx context.Context, repoName, repoURL, stackPath, tfVersion, tgVersion string, auth transport.AuthMethod, workspacePath string) (*RunResult, error) {
	result := &RunResult{
		RunAt: time.Now(),
	}

	tmpDir, err := os.MkdirTemp("", "driftd-*")
	if err != nil {
		result.Error = fmt.Sprintf("failed to create temp dir: %v", err)
		return result, nil
	}
	defer os.RemoveAll(tmpDir)

	if err := prepareWorkspace(ctx, tmpDir, repoURL, workspacePath, auth); err != nil {
		result.Error = err.Error()
		return result, nil
	}

	if !pathutil.IsSafeStackPath(stackPath) {
		result.Error = "invalid stack path"
		return result, nil
	}

	workDir := filepath.Join(tmpDir, stackPath)
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		result.Error = fmt.Sprintf("stack path not found: %s", stackPath)
		return result, nil
	}

	output, err := planStack(ctx, workDir, tmpDir, stackPath, tfVersion, tgVersion)
	result.PlanOutput = RedactPlanOutput(output)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// Exit code 2 means changes detected (drift)
			if exitErr.ExitCode() == 2 {
				result.Drifted = true
				result.Added, result.Changed, result.Destroyed = parsePlanSummary(output)
			} else {
				result.Error = fmt.Sprintf("plan failed with exit code %d", exitErr.ExitCode())
			}
		} else {
			result.Error = fmt.Sprintf("plan failed: %v", err)
		}
	} else {
		// Exit code 0 - check if there are still changes (some tf versions)
		result.Added, result.Changed, result.Destroyed = parsePlanSummary(output)
		result.Drifted = result.Added > 0 || result.Changed > 0 || result.Destroyed > 0
	}

	// Save to storage
	storageResult := &storage.RunResult{
		Drifted:    result.Drifted,
		Added:      result.Added,
		Changed:    result.Changed,
		Destroyed:  result.Destroyed,
		PlanOutput: result.PlanOutput,
		Error:      result.Error,
		RunAt:      result.RunAt,
	}
	if saveErr := r.storage.SaveResult(repoName, stackPath, storageResult); saveErr != nil {
		return result, fmt.Errorf("failed to save result: %w", saveErr)
	}

	return result, nil
}
