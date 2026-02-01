package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cbrown132/driftd/internal/storage"
	"github.com/go-git/go-git/v5"
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

func (r *Runner) Run(ctx context.Context, repoName, repoURL, stackPath, tfVersion, tgVersion string) (*RunResult, error) {
	result := &RunResult{
		RunAt: time.Now(),
	}

	tmpDir, err := os.MkdirTemp("", "driftd-*")
	if err != nil {
		result.Error = fmt.Sprintf("failed to create temp dir: %v", err)
		return result, nil
	}
	defer os.RemoveAll(tmpDir)

	_, err = git.PlainCloneContext(ctx, tmpDir, false, &git.CloneOptions{
		URL:   repoURL,
		Depth: 1,
	})
	if err != nil {
		result.Error = fmt.Sprintf("failed to clone repo: %v", err)
		return result, nil
	}

	workDir := filepath.Join(tmpDir, stackPath)
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		result.Error = fmt.Sprintf("stack path not found: %s", stackPath)
		return result, nil
	}

	tool := detectTool(workDir)

	tfBin, err := ensureTerraformBinary(ctx, workDir, tfVersion)
	if err != nil {
		result.Error = fmt.Sprintf("failed to install terraform: %v", err)
		return result, nil
	}

	var tgBin string
	if tool == "terragrunt" {
		tgBin, err = ensureTerragruntBinary(ctx, workDir, tgVersion)
		if err != nil {
			result.Error = fmt.Sprintf("failed to install terragrunt: %v", err)
			return result, nil
		}
	}

	output, err := runPlan(ctx, workDir, tool, tfBin, tgBin)
	result.PlanOutput = output

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

func detectTool(stackPath string) string {
	tgPath := filepath.Join(stackPath, "terragrunt.hcl")
	if _, err := os.Stat(tgPath); err == nil {
		return "terragrunt"
	}
	return "terraform"
}

func runPlan(ctx context.Context, workDir, tool, tfBin, tgBin string) (string, error) {
	var output bytes.Buffer

	if tool == "terraform" {
		initCmd := exec.CommandContext(ctx, tfBin, "init", "-input=false")
		initCmd.Dir = workDir
		initCmd.Stdout = &output
		initCmd.Stderr = &output
		if err := initCmd.Run(); err != nil {
			return output.String(), fmt.Errorf("terraform init failed: %w", err)
		}
	}

	var planCmd *exec.Cmd
	if tool == "terragrunt" {
		planCmd = exec.CommandContext(ctx, tgBin, "plan", "-detailed-exitcode", "-input=false")
		planCmd.Env = append(os.Environ(), fmt.Sprintf("TERRAGRUNT_TFPATH=%s", tfBin))
	} else {
		planCmd = exec.CommandContext(ctx, tfBin, "plan", "-detailed-exitcode", "-input=false")
	}
	planCmd.Dir = workDir
	planCmd.Stdout = &output
	planCmd.Stderr = &output

	err := planCmd.Run()
	return output.String(), err
}

var planSummaryRegex = regexp.MustCompile(`Plan: (\d+) to add, (\d+) to change, (\d+) to destroy`)

func parsePlanSummary(output string) (added, changed, destroyed int) {
	matches := planSummaryRegex.FindStringSubmatch(output)
	if len(matches) == 4 {
		added, _ = strconv.Atoi(matches[1])
		changed, _ = strconv.Atoi(matches[2])
		destroyed, _ = strconv.Atoi(matches[3])
	}

	if strings.Contains(output, "No changes.") || strings.Contains(output, "no differences") {
		return 0, 0, 0
	}

	return added, changed, destroyed
}
