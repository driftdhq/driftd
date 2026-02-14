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

// RunParams contains all parameters needed to execute a plan.
type RunParams struct {
	ProjectName   string
	ProjectURL    string
	StackPath     string
	TFVersion     string
	TGVersion     string
	RunID         string
	Auth          transport.AuthMethod
	WorkspacePath string
}

func (r *Runner) Run(ctx context.Context, params *RunParams) (*storage.RunResult, error) {
	result := &storage.RunResult{
		RunAt: time.Now(),
	}

	if !pathutil.IsSafeStackPath(params.StackPath) {
		result.Error = "invalid stack path"
		return result, nil
	}

	projectRoot, cleanup, err := r.prepareProjectRoot(ctx, params.ProjectURL, params.WorkspacePath, params.Auth)
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	if cleanup != nil {
		defer cleanup()
	}

	workDir := filepath.Join(projectRoot, params.StackPath)
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		result.Error = fmt.Sprintf("stack path not found: %s", params.StackPath)
		return result, nil
	}

	output, err := planStack(ctx, workDir, projectRoot, params.StackPath, params.TFVersion, params.TGVersion, params.RunID)
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

	if saveErr := r.storage.SaveResult(params.ProjectName, params.StackPath, result); saveErr != nil {
		return result, fmt.Errorf("failed to save result: %w", saveErr)
	}

	return result, nil
}
