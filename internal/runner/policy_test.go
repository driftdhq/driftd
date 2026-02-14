package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectExternalDataSource(t *testing.T) {
	t.Run("detects external data source in tf", func(t *testing.T) {
		stack := t.TempDir()
		tf := `
data "external" "test" {
  program = ["bash", "-c", "echo hi"]
}
`
		if err := os.WriteFile(filepath.Join(stack, "main.tf"), []byte(tf), 0644); err != nil {
			t.Fatalf("write tf: %v", err)
		}

		found, _, err := detectExternalDataSource(stack)
		if err != nil {
			t.Fatalf("detect failed: %v", err)
		}
		if !found {
			t.Fatalf("expected external data source to be detected")
		}
	})

	t.Run("ignores comments", func(t *testing.T) {
		stack := t.TempDir()
		tf := `
# data "external" "disabled" {}
/*
data "external" "disabled2" {}
*/
data "aws_caller_identity" "current" {}
`
		if err := os.WriteFile(filepath.Join(stack, "main.tf"), []byte(tf), 0644); err != nil {
			t.Fatalf("write tf: %v", err)
		}

		found, _, err := detectExternalDataSource(stack)
		if err != nil {
			t.Fatalf("detect failed: %v", err)
		}
		if found {
			t.Fatalf("did not expect detection for commented blocks")
		}
	})
}

func TestEnforceExternalDataSourcePolicy(t *testing.T) {
	stack := t.TempDir()
	tf := `
data "external" "blocked" {
  program = ["bash", "-c", "echo hi"]
}
`
	if err := os.WriteFile(filepath.Join(stack, "main.tf"), []byte(tf), 0644); err != nil {
		t.Fatalf("write tf: %v", err)
	}

	if err := enforceExternalDataSourcePolicy(stack, false); err != nil {
		t.Fatalf("policy should allow when disabled, got %v", err)
	}
	if err := enforceExternalDataSourcePolicy(stack, true); err == nil {
		t.Fatalf("policy should block when enabled")
	}
}
