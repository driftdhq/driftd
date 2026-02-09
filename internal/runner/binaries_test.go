package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionLockReturnsSameInstance(t *testing.T) {
	lockA := versionLock(&tfInstallLocks, "1.2.3")
	lockB := versionLock(&tfInstallLocks, "1.2.3")
	if lockA != lockB {
		t.Fatal("expected same lock for same version")
	}

	lockC := versionLock(&tfInstallLocks, "2.0.0")
	if lockA == lockC {
		t.Fatal("expected different lock for different version")
	}
}

func TestEnsureTerraformBinaryUsesTempSwitchDir(t *testing.T) {
	tmp := t.TempDir()
	workDir := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}

	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bindir: %v", err)
	}

	script := filepath.Join(binDir, "tfswitch")
	scriptBody := `#!/bin/sh
if [ "$1" = "-b" ]; then
  target="$2"
  shift 2
fi
if [ -n "$target" ]; then
  pwd > "${target}.cwd"
fi
mkdir -p "$(dirname "$target")"
echo '#!/bin/sh' > "$target"
chmod +x "$target"
exit 0
`
	if err := os.WriteFile(script, []byte(scriptBody), 0755); err != nil {
		t.Fatalf("write tfswitch script: %v", err)
	}

	oldPath := os.Getenv("PATH")
	oldSwitchHome := os.Getenv("TFSWITCH_HOME")
	t.Cleanup(func() {
		_ = os.Setenv("PATH", oldPath)
		_ = os.Setenv("TFSWITCH_HOME", oldSwitchHome)
	})

	cacheDir := filepath.Join(tmp, "cache")
	if err := os.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	if err := os.Setenv("TFSWITCH_HOME", cacheDir); err != nil {
		t.Fatalf("set TFSWITCH_HOME: %v", err)
	}
	target, err := ensureTerraformBinary(context.Background(), workDir, "1.2.3")
	if err != nil {
		t.Fatalf("ensureTerraformBinary: %v", err)
	}
	if !strings.HasPrefix(target, cacheDir) {
		t.Fatalf("expected target in cache dir, got %s", target)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected target binary to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".terraform-version")); err == nil {
		t.Fatal("did not expect .terraform-version in shared workspace")
	}
	cwdRaw, err := os.ReadFile(target + ".cwd")
	if err != nil {
		t.Fatalf("read switch cwd: %v", err)
	}
	switchCwd := strings.TrimSpace(string(cwdRaw))
	if switchCwd == "" {
		t.Fatal("expected switch cwd to be recorded")
	}
	if switchCwd == workDir {
		t.Fatal("expected tfswitch to run in temp dir, not workspace")
	}
}
