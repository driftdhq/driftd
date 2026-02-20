package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load default failed: %v", err)
	}

	if cfg.Worker.LockTTL < 2*time.Minute {
		t.Fatalf("expected lock_ttl >= 2m, got %s", cfg.Worker.LockTTL)
	}
	if cfg.Worker.RenewEvery <= 0 {
		t.Fatalf("expected renew_every to be set")
	}
	if cfg.Worker.RenewEvery > cfg.Worker.LockTTL/2 {
		t.Fatalf("expected renew_every <= lock_ttl/2")
	}
	if cfg.Worker.CloneDepth != 1 {
		t.Fatalf("expected clone_depth default 1, got %d", cfg.Worker.CloneDepth)
	}
}

func TestLoadValidation(t *testing.T) {
	t.Run("lock_ttl_too_small", func(t *testing.T) {
		path := writeTempConfig(t, "worker:\n  lock_ttl: 30s\n")
		if _, err := Load(path); err == nil {
			t.Fatalf("expected error for small lock_ttl")
		}
	})

	t.Run("renew_every_too_small", func(t *testing.T) {
		path := writeTempConfig(t, "worker:\n  lock_ttl: 2m\n  renew_every: 5s\n")
		if _, err := Load(path); err == nil {
			t.Fatalf("expected error for small renew_every")
		}
	})

	t.Run("renew_every_too_large", func(t *testing.T) {
		path := writeTempConfig(t, "worker:\n  lock_ttl: 2m\n  renew_every: 90s\n")
		if _, err := Load(path); err == nil {
			t.Fatalf("expected error for large renew_every")
		}
	})

	t.Run("block_external_data_source_flag", func(t *testing.T) {
		path := writeTempConfig(t, "worker:\n  block_external_data_source: true\n")
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if !cfg.Worker.BlockExternalDataSource {
			t.Fatalf("expected worker.block_external_data_source=true")
		}
	})

	t.Run("clone_depth_configured", func(t *testing.T) {
		path := writeTempConfig(t, "worker:\n  clone_depth: 5\n")
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Worker.CloneDepth != 5 {
			t.Fatalf("expected worker.clone_depth=5, got %d", cfg.Worker.CloneDepth)
		}
	})

	t.Run("clone_depth_invalid", func(t *testing.T) {
		path := writeTempConfig(t, "worker:\n  clone_depth: -1\n")
		if _, err := Load(path); err == nil {
			t.Fatalf("expected error for invalid clone_depth")
		}
	})

	t.Run("clone_depth_too_large", func(t *testing.T) {
		path := writeTempConfig(t, "worker:\n  clone_depth: 1001\n")
		if _, err := Load(path); err == nil {
			t.Fatalf("expected error for too-large clone_depth")
		}
	})

	t.Run("cancel_inflight_defaults_true", func(t *testing.T) {
		path := writeTempConfig(t, "projects:\n  - name: project\n    url: https://example.com/project.git\n")
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if !cfg.Projects[0].CancelInflightEnabled() {
			t.Fatalf("expected cancel_inflight_on_new_trigger default true")
		}
	})

	t.Run("cancel_inflight_false", func(t *testing.T) {
		path := writeTempConfig(t, "projects:\n  - name: project\n    url: https://example.com/project.git\n    cancel_inflight_on_new_trigger: false\n")
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Projects[0].CancelInflightEnabled() {
			t.Fatalf("expected cancel_inflight_on_new_trigger false")
		}
	})

	t.Run("cleanup_after_plan_defaults_true", func(t *testing.T) {
		path := writeTempConfig(t, "projects:\n  - name: project\n    url: https://example.com/project.git\n")
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if !cfg.Workspace.CleanupAfterPlanEnabled() {
			t.Fatalf("expected cleanup_after_plan default true")
		}
	})

	t.Run("cleanup_after_plan_false", func(t *testing.T) {
		path := writeTempConfig(t, "workspace:\n  cleanup_after_plan: false\nrepos:\n  - name: project\n    url: https://example.com/project.git\n")
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		if cfg.Workspace.CleanupAfterPlanEnabled() {
			t.Fatalf("expected cleanup_after_plan false")
		}
	})

	t.Run("monorepo_expands_projects", func(t *testing.T) {
		path := writeTempConfig(t, `
projects:
  - name: infra-monorepo
    url: https://example.com/infra.git
    branch: main
    schedule: "0 */6 * * *"
    ignore_paths:
      - "**/modules/**"
    cancel_inflight_on_new_trigger: false
    projects:
      - name: account-a
        path: aws/accountA
      - name: account-b
        path: aws/accountB
        schedule: "0 0 * * *"
        ignore_paths: []
`)
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}

		if len(cfg.Projects) != 2 {
			t.Fatalf("expected 2 expanded projects, got %d", len(cfg.Projects))
		}

		accountA := cfg.GetProject("account-a")
		if accountA == nil {
			t.Fatalf("expected account-a project")
		}
		if accountA.RootPath != "aws/accountA" {
			t.Fatalf("expected root path aws/accountA, got %q", accountA.RootPath)
		}
		if accountA.CloneURL != "https://example.com/infra.git" {
			t.Fatalf("expected clone url inherited, got %q", accountA.CloneURL)
		}
		if accountA.Schedule != "0 */6 * * *" {
			t.Fatalf("expected inherited schedule, got %q", accountA.Schedule)
		}
		if accountA.Branch != "main" {
			t.Fatalf("expected branch inherited, got %q", accountA.Branch)
		}
		if accountA.CancelInflightEnabled() {
			t.Fatalf("expected cancel_inflight inherited as false")
		}
		if len(accountA.IgnorePaths) != 1 || accountA.IgnorePaths[0] != "**/modules/**" {
			t.Fatalf("expected ignore paths inherited, got %v", accountA.IgnorePaths)
		}

		accountB := cfg.GetProject("account-b")
		if accountB == nil {
			t.Fatalf("expected account-b project")
		}
		if accountB.Schedule != "0 0 * * *" {
			t.Fatalf("expected schedule override, got %q", accountB.Schedule)
		}
		if accountB.IgnorePaths == nil || len(accountB.IgnorePaths) != 0 {
			t.Fatalf("expected empty ignore override, got %v", accountB.IgnorePaths)
		}

		if cfg.GetProject("infra-monorepo") != nil {
			t.Fatalf("parent project should not be scannable after expansion")
		}
	})

	t.Run("monorepo_rejects_overlapping_paths", func(t *testing.T) {
		path := writeTempConfig(t, `
projects:
  - name: infra-monorepo
    url: https://example.com/infra.git
    projects:
      - name: project-a
        path: aws
      - name: project-b
        path: aws/accountB
`)
		if _, err := Load(path); err == nil {
			t.Fatalf("expected overlap validation error")
		}
	})

	t.Run("monorepo_rejects_unsafe_path", func(t *testing.T) {
		path := writeTempConfig(t, `
projects:
  - name: infra-monorepo
    url: https://example.com/infra.git
    projects:
      - name: project-a
        path: ../aws
`)
		if _, err := Load(path); err == nil {
			t.Fatalf("expected unsafe path validation error")
		}
	})

	t.Run("monorepo_rejects_duplicate_expanded_names", func(t *testing.T) {
		path := writeTempConfig(t, `
projects:
  - name: project-a
    url: https://example.com/single.git
  - name: infra-monorepo
    url: https://example.com/infra.git
    projects:
      - name: project-a
        path: aws/accountA
`)
		if _, err := Load(path); err == nil {
			t.Fatalf("expected duplicate expanded name error")
		}
	})
}

func writeTempConfig(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
