package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPlan_RetriesOnProviderChecksumMismatchWithIsolatedCache(t *testing.T) {
	tmp := t.TempDir()
	workDir := filepath.Join(tmp, "work")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir workDir: %v", err)
	}
	repoRoot := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repoRoot, 0755); err != nil {
		t.Fatalf("mkdir repoRoot: %v", err)
	}

	sharedCache := filepath.Join(tmp, "shared-plugin-cache")
	if err := os.MkdirAll(sharedCache, 0755); err != nil {
		t.Fatalf("mkdir sharedCache: %v", err)
	}

	logPath := filepath.Join(tmp, "tf.log")
	tfBin := filepath.Join(tmp, "terraform")

	// Fake terraform that fails init when using the shared plugin cache to simulate
	// a corrupted/stale provider package, then succeeds on retry.
	script := `#!/bin/sh
set -eu
cmd="$1"
shift || true

echo "CMD=${cmd} ARGS=$*" >> "` + logPath + `"
echo "TF_PLUGIN_CACHE_DIR=${TF_PLUGIN_CACHE_DIR:-}" >> "` + logPath + `"
echo "TF_DATA_DIR=${TF_DATA_DIR:-}" >> "` + logPath + `"

if [ "$cmd" = "init" ]; then
  case "${TF_PLUGIN_CACHE_DIR:-}" in
    "` + sharedCache + `")
      echo "Error: Required plugins are not installed"
      echo "does not match any of the checksums recorded in the dependency lock file"
      exit 1
      ;;
  esac
  # Retry path uses -upgrade.
  echo "$*" | grep -q -- "-upgrade" || exit 3
  echo "Terraform has been successfully initialized!"
  exit 0
fi

if [ "$cmd" = "plan" ]; then
  echo "No changes."
  exit 0
fi

exit 0
`
	if err := os.WriteFile(tfBin, []byte(script), 0755); err != nil {
		t.Fatalf("write terraform script: %v", err)
	}

	t.Setenv("TF_PLUGIN_CACHE_DIR", sharedCache)

	out, err := runPlan(context.Background(), workDir, "terraform", tfBin, "", repoRoot, "envs/dev/app", "run-1")
	if err != nil {
		t.Fatalf("runPlan error: %v\noutput:\n%s", err, out)
	}
	if !strings.Contains(out, "--- retry (fresh plugin cache) ---") {
		t.Fatalf("expected retry marker in output, got:\n%s", out)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	log := string(logBytes)

	// Two init attempts, one plan (only after init succeeds).
	if got := strings.Count(log, "CMD=init"); got != 2 {
		t.Fatalf("expected 2 init attempts, got %d\nlog:\n%s", got, log)
	}
	if got := strings.Count(log, "CMD=plan"); got != 1 {
		t.Fatalf("expected 1 plan run, got %d\nlog:\n%s", got, log)
	}

	// Ensure we actually switched away from shared cache on retry.
	lines := strings.Split(log, "\n")
	var pluginCacheDirs []string
	var dataDirs []string
	for _, line := range lines {
		if strings.HasPrefix(line, "TF_PLUGIN_CACHE_DIR=") {
			pluginCacheDirs = append(pluginCacheDirs, strings.TrimPrefix(line, "TF_PLUGIN_CACHE_DIR="))
		}
		if strings.HasPrefix(line, "TF_DATA_DIR=") {
			dataDirs = append(dataDirs, strings.TrimPrefix(line, "TF_DATA_DIR="))
		}
	}
	if len(pluginCacheDirs) < 2 {
		t.Fatalf("expected at least 2 TF_PLUGIN_CACHE_DIR entries\nlog:\n%s", log)
	}
	if pluginCacheDirs[0] != sharedCache {
		t.Fatalf("expected first attempt to use shared cache %q, got %q", sharedCache, pluginCacheDirs[0])
	}
	if pluginCacheDirs[1] == sharedCache {
		t.Fatalf("expected retry to use isolated cache, but got shared cache again\nlog:\n%s", log)
	}

	// Unique TF_DATA_DIR per attempt.
	if len(dataDirs) < 2 {
		t.Fatalf("expected at least 2 TF_DATA_DIR entries\nlog:\n%s", log)
	}
	if dataDirs[0] == dataDirs[1] {
		t.Fatalf("expected different TF_DATA_DIR per attempt, got same %q\nlog:\n%s", dataDirs[0], log)
	}
}
