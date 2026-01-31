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
