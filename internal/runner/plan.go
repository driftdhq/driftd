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

func detectTool(stackDir string) string {
	tgPath := filepath.Join(stackDir, "terragrunt.hcl")
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
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return "", fmt.Errorf("create TF_DATA_DIR: %w", err)
	}
	defer os.RemoveAll(dataDir)

	pluginCacheDir := os.Getenv("TF_PLUGIN_CACHE_DIR")
	if pluginCacheDir == "" {
		pluginCacheDir = "/cache/terraform/plugins"
	}
	if err := os.MkdirAll(pluginCacheDir, 0755); err != nil {
		// Fallback to per-run cache; slower but better than failing the plan.
		pluginCacheDir = filepath.Join(dataDir, "plugin-cache")
		_ = os.MkdirAll(pluginCacheDir, 0755)
	}

	var tgDownloadDir string
	if tool == "terragrunt" {
		// Per-run dir; avoid collisions between concurrent scans.
		tgDownloadDir = filepath.Join(os.TempDir(), "driftd-tg", safePath(dataKey), safePath(stackPath))
		if err := os.MkdirAll(tgDownloadDir, 0755); err == nil {
			defer os.RemoveAll(tgDownloadDir)
		}
	}

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
			fmt.Sprintf("TG_DOWNLOAD_DIR=%s", tgDownloadDir),
			fmt.Sprintf("TERRAGRUNT_TFPATH=%s", tfBin),
			fmt.Sprintf("TERRAGRUNT_DOWNLOAD=%s", tgDownloadDir),
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
	allowed := map[string]struct{}{
		"PATH":               {},
		"HOME":               {},
		"TMPDIR":             {},
		"LANG":               {},
		"LC_ALL":             {},
		"SSL_CERT_FILE":      {},
		"SSL_CERT_DIR":       {},
		"REQUESTS_CA_BUNDLE": {},
		"CURL_CA_BUNDLE":     {},
		// Proxy vars (often needed for provider/network access).
		"HTTP_PROXY":  {},
		"HTTPS_PROXY": {},
		"NO_PROXY":    {},
		"http_proxy":  {},
		"https_proxy": {},
		"no_proxy":    {},
		// Git/SSH commonly needed for modules.
		"SSH_AUTH_SOCK":   {},
		"GIT_SSH":         {},
		"GIT_SSH_COMMAND": {},
	}
	allowedPrefixes := []string{
		"TF_",
		"TERRAGRUNT_",
		// Common cloud/provider credentials.
		"AWS_",
		"GOOGLE_",
		"ARM_",
		"AZURE_",
		"CLOUDFLARE_",
		"DIGITALOCEAN_",
		"OCI_",
		// Kubernetes related (for some providers/backends).
		"KUBE",
		// Git credentials for module sources (e.g. GIT_ASKPASS, GIT_TERMINAL_PROMPT).
		"GIT_",
		// CI context can affect terraform/terragrunt behaviors.
		"CI",
	}
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		if _, ok := allowed[key]; ok {
			out = append(out, entry)
			continue
		}
		for _, pfx := range allowedPrefixes {
			if strings.HasPrefix(key, pfx) {
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
