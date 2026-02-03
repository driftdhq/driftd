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

	"github.com/driftdhq/driftd/internal/storage"
	"github.com/go-git/go-git/v5"
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

	if workspacePath != "" {
		if err := copyRepo(workspacePath, tmpDir); err != nil {
			result.Error = fmt.Sprintf("failed to copy workspace from %s: %v", workspacePath, err)
			return result, nil
		}
	} else {
		_, err = git.PlainCloneContext(ctx, tmpDir, false, &git.CloneOptions{
			URL:   repoURL,
			Depth: 1,
			Auth:  auth,
		})
		if err != nil {
			result.Error = fmt.Sprintf("failed to clone repo: %v", err)
			return result, nil
		}
	}

	if !isSafeStackPath(stackPath) {
		result.Error = "invalid stack path"
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
	tfBin, err = ensurePlanOnlyWrapper(workDir, tfBin)
	if err != nil {
		result.Error = fmt.Sprintf("failed to create terraform wrapper: %v", err)
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

	output, err := runPlan(ctx, workDir, tool, tfBin, tgBin, tmpDir, stackPath)
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

func runPlan(ctx context.Context, workDir, tool, tfBin, tgBin, repoRoot, stackPath string) (string, error) {
	var output bytes.Buffer
	dataDir := filepath.Join(os.TempDir(), "driftd-tfdata", safePath(stackPath), filepath.Base(repoRoot))
	if err := os.MkdirAll(dataDir, 0755); err == nil {
		defer os.RemoveAll(dataDir)
	}
	pluginCacheDir := filepath.Join(dataDir, "plugin-cache")
	ensureCacheDir(pluginCacheDir)

	if tool == "terraform" {
		initCmd := exec.CommandContext(ctx, tfBin, "init", "-input=false")
		initCmd.Dir = workDir
		initCmd.Env = append(filteredEnv(),
			fmt.Sprintf("TF_DATA_DIR=%s", dataDir),
			fmt.Sprintf("TF_PLUGIN_CACHE_DIR=%s", pluginCacheDir),
		)
		initCmd.Stdout = &output
		initCmd.Stderr = &output
		if err := initCmd.Run(); err != nil {
			return output.String(), fmt.Errorf("terraform init failed: %w", err)
		}
	}

	var planCmd *exec.Cmd
	if tool == "terragrunt" {
		planCmd = exec.CommandContext(ctx, tgBin, "plan", "-detailed-exitcode", "-input=false")
		planCmd.Env = append(filteredEnv(),
			fmt.Sprintf("TERRAGRUNT_TFPATH=%s", tfBin),
			fmt.Sprintf("TERRAGRUNT_DOWNLOAD=%s", filepath.Join(os.TempDir(), "driftd-tg", safePath(stackPath))),
			fmt.Sprintf("TF_DATA_DIR=%s", dataDir),
			fmt.Sprintf("TF_PLUGIN_CACHE_DIR=%s", pluginCacheDir),
		)
	} else {
		planCmd = exec.CommandContext(ctx, tfBin, "plan", "-detailed-exitcode", "-input=false")
		planCmd.Env = append(filteredEnv(),
			fmt.Sprintf("TF_DATA_DIR=%s", dataDir),
			fmt.Sprintf("TF_PLUGIN_CACHE_DIR=%s", pluginCacheDir),
		)
	}
	planCmd.Dir = workDir
	planCmd.Stdout = &output
	planCmd.Stderr = &output

	err := planCmd.Run()
	return output.String(), err
}

func ensureCacheDir(path string) {
	if path == "" {
		path = "/cache/terraform/plugins"
	}
	_ = os.MkdirAll(path, 0755)
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

func filteredEnv() []string {
	allowed := []string{"PATH", "HOME", "TMPDIR"}
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		if strings.HasPrefix(key, "TF_") || strings.HasPrefix(key, "TERRAGRUNT_") {
			out = append(out, entry)
			continue
		}
		for _, allow := range allowed {
			if key == allow {
				out = append(out, entry)
				break
			}
		}
	}
	return out
}

func safePath(path string) string {
	return strings.ReplaceAll(path, string(os.PathSeparator), "__")
}

func isSafeStackPath(stackPath string) bool {
	if stackPath == "" {
		return true
	}
	if filepath.IsAbs(stackPath) {
		return false
	}
	clean := filepath.Clean(stackPath)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}
