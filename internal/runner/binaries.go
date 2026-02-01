package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var tfInstallMu sync.Mutex
var tgInstallMu sync.Mutex

func ensureTerraformBinary(ctx context.Context, workDir, version string) (string, error) {
	if version == "" {
		return "terraform", nil
	}
	cacheDir := getenv("TFSWITCH_HOME", "/cache/terraform/versions")
	target := filepath.Join(cacheDir, version, "terraform")
	if fileExists(target) {
		return target, nil
	}

	tfInstallMu.Lock()
	defer tfInstallMu.Unlock()

	if fileExists(target) {
		return target, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", err
	}

	restore, err := ensureVersionFile(workDir, ".terraform-version", version)
	if err != nil {
		return "", err
	}
	if restore != nil {
		defer restore()
	}

	if err := runSwitch(ctx, workDir, "tfswitch", cacheDir, target); err != nil {
		return "", err
	}
	if !fileExists(target) {
		return "", fmt.Errorf("terraform binary not found after tfswitch")
	}
	return target, nil
}

func ensureTerragruntBinary(ctx context.Context, workDir, version string) (string, error) {
	if version == "" {
		return "terragrunt", nil
	}
	cacheDir := getenv("TGSWITCH_HOME", "/cache/terragrunt/versions")
	target := filepath.Join(cacheDir, version, "terragrunt")
	if fileExists(target) {
		return target, nil
	}

	tgInstallMu.Lock()
	defer tgInstallMu.Unlock()

	if fileExists(target) {
		return target, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", err
	}

	restore, err := ensureVersionFile(workDir, ".terragrunt-version", version)
	if err != nil {
		return "", err
	}
	if restore != nil {
		defer restore()
	}

	if err := runSwitch(ctx, workDir, "tgswitch", cacheDir, target); err != nil {
		return "", err
	}
	if !fileExists(target) {
		return "", fmt.Errorf("terragrunt binary not found after tgswitch")
	}
	return target, nil
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

func runSwitch(ctx context.Context, workDir, switchCmd, cacheDir, target string) error {
	cmd := exec.CommandContext(ctx, switchCmd, "-b", target)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
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

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Chmod(0755)
}

func copyRepo(src, dst string) error {
	return copyDir(src, dst, map[string]struct{}{".git": {}})
}

func copyDir(src, dst string, skip map[string]struct{}) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, info.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if _, ok := skip[name]; ok {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		srcPath := filepath.Join(src, name)
		dstPath := filepath.Join(dst, name)
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(srcPath)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, dstPath); err != nil {
				return err
			}
			continue
		}
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath, skip); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
