package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var tfInstallLocks sync.Map
var tgInstallLocks sync.Map

func versionLock(store *sync.Map, key string) *sync.Mutex {
	if key == "" {
		key = "default"
	}
	if existing, ok := store.Load(key); ok {
		return existing.(*sync.Mutex)
	}
	lock := &sync.Mutex{}
	actual, _ := store.LoadOrStore(key, lock)
	return actual.(*sync.Mutex)
}

func ensureTerraformBinary(ctx context.Context, workDir, version string) (string, error) {
	if version == "" {
		if defaultVersion := os.Getenv("DRIFTD_DEFAULT_TERRAFORM_VERSION"); defaultVersion != "" {
			version = defaultVersion
		} else if path, err := exec.LookPath("terraform"); err == nil {
			if !filepath.IsAbs(path) {
				if abs, err := filepath.Abs(path); err == nil {
					path = abs
				}
			}
			if fileExists(path) {
				return path, nil
			}
		} else {
			return "", fmt.Errorf("terraform not found; set DRIFTD_DEFAULT_TERRAFORM_VERSION or install terraform in PATH")
		}
	}
	cacheDir := getenv("TFSWITCH_HOME", "/cache/terraform/versions")
	target := filepath.Join(cacheDir, version, "terraform")
	if fileExists(target) {
		return target, nil
	}

	lock := versionLock(&tfInstallLocks, version)
	lock.Lock()
	defer lock.Unlock()

	if fileExists(target) {
		return target, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", err
	}

	tmpDir, err := os.MkdirTemp("", "driftd-switch-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)
	switchDir := tmpDir

	restore, err := ensureVersionFile(switchDir, ".terraform-version", version)
	if err != nil {
		return "", err
	}
	if restore != nil {
		defer restore()
	}
	if err := runSwitch(ctx, switchDir, "tfswitch", cacheDir, target, version); err != nil {
		return "", err
	}
	if !fileExists(target) {
		return "", fmt.Errorf("terraform binary not found after tfswitch")
	}
	return target, nil
}

func ensureTerragruntBinary(ctx context.Context, workDir, version string) (string, error) {
	if version == "" {
		if defaultVersion := os.Getenv("DRIFTD_DEFAULT_TERRAGRUNT_VERSION"); defaultVersion != "" {
			version = defaultVersion
		} else if path, err := exec.LookPath("terragrunt"); err == nil {
			if !filepath.IsAbs(path) {
				if abs, err := filepath.Abs(path); err == nil {
					path = abs
				}
			}
			if fileExists(path) {
				return path, nil
			}
		} else {
			return "", fmt.Errorf("terragrunt not found; set DRIFTD_DEFAULT_TERRAGRUNT_VERSION or install terragrunt in PATH")
		}
	}
	cacheDir := getenv("TGSWITCH_HOME", "/cache/terragrunt/versions")
	target := filepath.Join(cacheDir, version, "terragrunt")
	if fileExists(target) {
		return target, nil
	}

	lock := versionLock(&tgInstallLocks, version)
	lock.Lock()
	defer lock.Unlock()

	if fileExists(target) {
		return target, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", err
	}

	tmpDir, err := os.MkdirTemp("", "driftd-switch-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)
	switchDir := tmpDir

	restore, err := ensureVersionFile(switchDir, ".terragrunt-version", version)
	if err != nil {
		return "", err
	}
	if restore != nil {
		defer restore()
	}
	if err := runSwitch(ctx, switchDir, "tgswitch", cacheDir, target, version); err != nil {
		return "", err
	}
	if !fileExists(target) {
		return "", fmt.Errorf("terragrunt binary not found after tgswitch")
	}
	return target, nil
}

func EnsureDefaultBinaries(ctx context.Context) error {
	tfVersion := os.Getenv("DRIFTD_DEFAULT_TERRAFORM_VERSION")
	tgVersion := os.Getenv("DRIFTD_DEFAULT_TERRAGRUNT_VERSION")

	if tfVersion == "" && tgVersion == "" {
		return nil
	}

	workDir, err := os.MkdirTemp("", "driftd-defaults-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	if tfVersion != "" {
		if _, err := ensureTerraformBinary(ctx, workDir, tfVersion); err != nil {
			return fmt.Errorf("install default terraform %s: %w", tfVersion, err)
		}
	}
	if tgVersion != "" {
		if _, err := ensureTerragruntBinary(ctx, workDir, tgVersion); err != nil {
			return fmt.Errorf("install default terragrunt %s: %w", tgVersion, err)
		}
	}

	return nil
}

func ensurePlanOnlyWrapper(workDir, tfBin string) (string, error) {
	wrapperPath, err := planOnlyWrapperPath(workDir, tfBin)
	if err != nil {
		return "", err
	}
	if planOnlyWrapperValid(wrapperPath, tfBin) {
		return wrapperPath, nil
	}

	if err := os.MkdirAll(filepath.Dir(wrapperPath), 0755); err != nil {
		return "", err
	}

	selfBin, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve current executable: %w", err)
	}

	if err := os.WriteFile(planOnlyTargetPath(wrapperPath), []byte(tfBin+"\n"), 0644); err != nil {
		return "", err
	}

	_ = os.Remove(wrapperPath)
	if err := os.Symlink(selfBin, wrapperPath); err != nil {
		if linkErr := os.Link(selfBin, wrapperPath); linkErr != nil {
			return "", fmt.Errorf("create plan-only wrapper link: %w", err)
		}
	}

	if !planOnlyWrapperValid(wrapperPath, tfBin) {
		return "", fmt.Errorf("plan-only wrapper validation failed")
	}
	return wrapperPath, nil
}

func planOnlyTargetPath(wrapperPath string) string {
	return wrapperPath + ".target"
}

func planOnlyWrapperValid(wrapperPath, tfBin string) bool {
	content, err := os.ReadFile(planOnlyTargetPath(wrapperPath))
	if err != nil {
		return false
	}
	if strings.TrimSpace(string(content)) != tfBin {
		return false
	}

	selfBin, err := os.Executable()
	if err != nil {
		return false
	}

	wrapperInfo, err := os.Lstat(wrapperPath)
	if err != nil {
		return false
	}

	if wrapperInfo.Mode()&os.ModeSymlink != 0 {
		dst, err := os.Readlink(wrapperPath)
		if err != nil {
			return false
		}
		if !filepath.IsAbs(dst) {
			dst = filepath.Join(filepath.Dir(wrapperPath), dst)
		}
		return filepath.Clean(dst) == filepath.Clean(selfBin)
	}

	linkedInfo, err := os.Stat(wrapperPath)
	if err != nil {
		return false
	}
	selfInfo, err := os.Stat(selfBin)
	if err != nil {
		return false
	}
	return os.SameFile(linkedInfo, selfInfo)
}

func readPlanOnlyTarget(wrapperPath string) (string, error) {
	content, err := os.ReadFile(planOnlyTargetPath(wrapperPath))
	if err != nil {
		return "", fmt.Errorf("read plan-only target: %w", err)
	}
	target := strings.TrimSpace(string(content))
	if target == "" {
		return "", fmt.Errorf("empty plan-only target")
	}
	if !fileExists(target) {
		return "", fmt.Errorf("plan-only target not found: %s", target)
	}
	return target, nil
}

func firstTerraformSubcommand(args []string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return arg
	}
	return ""
}

func isBlockedTerraformSubcommand(subcommand string) bool {
	switch subcommand {
	case "apply", "destroy", "import", "taint", "untaint", "state", "console", "login", "logout":
		return true
	default:
		return false
	}
}

func runPlanOnlyProxy(wrapperPath string, args []string, stdout, stderr *os.File) int {
	subcommand := firstTerraformSubcommand(args)
	if isBlockedTerraformSubcommand(subcommand) {
		_, _ = fmt.Fprintf(stderr, "driftd: terraform subcommand disabled: %s\n", subcommand)
		return 2
	}

	target, err := readPlanOnlyTarget(wrapperPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "driftd: %v\n", err)
		return 2
	}

	cmd := exec.Command(target, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		_, _ = fmt.Fprintf(stderr, "driftd: failed to execute terraform: %v\n", err)
		return 1
	}
	return 0
}

func MaybeRunPlanOnlyProxy(argv0 string, args []string) (bool, int) {
	if !strings.HasSuffix(filepath.Base(argv0), ".planonly") {
		return false, 0
	}
	return true, runPlanOnlyProxy(argv0, args, os.Stdout, os.Stderr)
}

func planOnlyWrapperPath(workDir, tfBin string) (string, error) {
	if filepath.IsAbs(tfBin) {
		return tfBin + ".planonly", nil
	}
	if workDir == "" {
		return "", fmt.Errorf("workDir required for plan-only wrapper")
	}
	return filepath.Join(workDir, ".driftd", "terraform.planonly"), nil
}

func runSwitch(ctx context.Context, workDir, switchCmd, cacheDir, target, version string) error {
	args := []string{"-b", target}
	if version != "" {
		args = append(args, version)
	}
	cmd := exec.CommandContext(ctx, switchCmd, args...)
	cmd.Dir = workDir
	cmd.Env = append(filteredEnv(),
		fmt.Sprintf("%s=%s", switchHomeEnv(switchCmd), cacheDir),
		fmt.Sprintf("PATH=%s%s%s", cacheDir, string(os.PathListSeparator), os.Getenv("PATH")),
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		if _, pathErr := exec.LookPath(switchCmd); pathErr != nil {
			return fmt.Errorf("%s not installed", switchCmd)
		}
		return fmt.Errorf("%s failed: %v (output: %s)", switchCmd, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func switchHomeEnv(switchCmd string) string {
	if switchCmd == "tgswitch" {
		return "TGSWITCH_HOME"
	}
	return "TFSWITCH_HOME"
}

func ensureVersionFile(workDir, fileName, version string) (func(), error) {
	path := filepath.Join(workDir, fileName)
	orig, err := os.ReadFile(path)
	if err == nil && strings.TrimSpace(string(orig)) == version {
		return nil, nil
	}

	if err := os.WriteFile(path, []byte(version), 0644); err != nil {
		return nil, err
	}

	return func() {
		if len(orig) == 0 {
			_ = os.Remove(path)
			return
		}
		_ = os.WriteFile(path, orig, 0644)
	}, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
