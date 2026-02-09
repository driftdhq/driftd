package runner

import (
	"context"
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
	if fileExists(wrapperPath) {
		return wrapperPath, nil
	}

	if err := os.MkdirAll(filepath.Dir(wrapperPath), 0755); err != nil {
		return "", err
	}

	script := fmt.Sprintf(`#!/bin/sh
set -eu
cmd=""
for arg in "$@"; do
  case "$arg" in
    -*) continue ;;
    *) cmd="$arg"; break ;;
  esac
done
case "$cmd" in
  apply|destroy|import|taint|untaint|state|console|login|logout)
    echo "driftd: terraform subcommand disabled: $cmd" >&2
    exit 2
    ;;
esac
exec %q "$@"
`, tfBin)

	if err := os.WriteFile(wrapperPath, []byte(script), 0755); err != nil {
		return "", err
	}
	return wrapperPath, nil
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
