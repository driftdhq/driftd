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
)

func planStack(ctx context.Context, workDir, repoRoot, stackPath, tfVersion, tgVersion, runID string) (string, error) {
	tool := detectTool(workDir)

	tfBin, err := ensureTerraformBinary(ctx, workDir, tfVersion)
	if err != nil {
		return "", fmt.Errorf("failed to install terraform: %v", err)
	}
	tfBin, err = ensurePlanOnlyWrapper(workDir, tfBin)
	if err != nil {
		return "", fmt.Errorf("failed to create terraform wrapper: %v", err)
	}

	var tgBin string
	if tool == "terragrunt" {
		tgBin, err = ensureTerragruntBinary(ctx, workDir, tgVersion)
		if err != nil {
			return "", fmt.Errorf("failed to install terragrunt: %v", err)
		}
	}

	return runPlan(ctx, workDir, tool, tfBin, tgBin, repoRoot, stackPath, runID)
}

func detectTool(stackPath string) string {
	tgPath := filepath.Join(stackPath, "terragrunt.hcl")
	if _, err := os.Stat(tgPath); err == nil {
		return "terragrunt"
	}
	return "terraform"
}

func runPlan(ctx context.Context, workDir, tool, tfBin, tgBin, repoRoot, stackPath, runID string) (string, error) {
	var output bytes.Buffer
	dataKey := runID
	if dataKey == "" {
		dataKey = filepath.Base(repoRoot)
	}
	dataDir := filepath.Join(os.TempDir(), "driftd-tfdata", safePath(stackPath), safePath(dataKey))
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
			fmt.Sprintf("TG_TF_PATH=%s", tfBin),
			fmt.Sprintf("TG_DOWNLOAD_DIR=%s", filepath.Join(os.TempDir(), "driftd-tg", safePath(stackPath))),
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
	return cleanTerragruntOutput(tool, output.String()), err
}

func ensureCacheDir(path string) {
	if path == "" {
		path = "/cache/terraform/plugins"
	}
	_ = os.MkdirAll(path, 0755)
}

func cleanTerragruntOutput(tool, output string) string {
	if tool != "terragrunt" {
		return output
	}
	lines := strings.Split(output, "\n")
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		raw := line
		line = strings.TrimPrefix(line, "STDOUT ")
		line = strings.TrimPrefix(line, "INFO   ")
		line = strings.TrimPrefix(line, "WARN   ")
		line = strings.TrimPrefix(line, "ERROR  ")
		if idx := strings.Index(line, "terraform.planonly: "); idx != -1 {
			line = line[idx+len("terraform.planonly: "):]
			cleaned = append(cleaned, line)
			continue
		}
		if idx := strings.Index(line, "terraform: "); idx != -1 {
			line = line[idx+len("terraform: "):]
			cleaned = append(cleaned, line)
			continue
		}
		if raw != line {
			continue
		}
		cleaned = append(cleaned, line)
	}
	return strings.Join(cleaned, "\n")
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
