package projects

import (
	"testing"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/secrets"
)

func TestCombinedProviderPrefersStaticConfig(t *testing.T) {
	cfg := &config.Config{
		Projects: []config.ProjectConfig{
			{Name: "project", URL: "https://example.com/static.git"},
		},
	}

	store := secrets.NewProjectStore(t.TempDir(), nil)
	// Add a dynamic project with the same name to verify static precedence.
	entry := &secrets.ProjectEntry{
		Name: "project",
		URL:  "https://example.com/dynamic.git",
	}
	if err := store.Add(entry, nil); err != nil {
		t.Fatalf("add entry: %v", err)
	}

	provider := NewCombinedProvider(cfg, store, nil, t.TempDir())

	got, err := provider.Get("project")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.URL != "https://example.com/static.git" {
		t.Fatalf("expected static project, got %s", got.URL)
	}
}

func TestCombinedProviderListIncludesDynamic(t *testing.T) {
	cfg := &config.Config{
		Projects: []config.ProjectConfig{
			{Name: "static", URL: "https://example.com/static.git"},
		},
	}

	store := secrets.NewProjectStore(t.TempDir(), nil)
	entry := &secrets.ProjectEntry{
		Name: "dynamic",
		URL:  "https://example.com/dynamic.git",
	}
	if err := store.Add(entry, nil); err != nil {
		t.Fatalf("add entry: %v", err)
	}

	provider := NewCombinedProvider(cfg, store, nil, t.TempDir())
	projects, err := provider.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
}

func TestCombinedProviderMissingIntegration(t *testing.T) {
	cfg := &config.Config{}

	store := secrets.NewProjectStore(t.TempDir(), nil)
	entry := &secrets.ProjectEntry{
		Name:          "dynamic",
		URL:           "https://example.com/dynamic.git",
		IntegrationID: "missing",
	}
	if err := store.Add(entry, nil); err != nil {
		t.Fatalf("add entry: %v", err)
	}

	provider := NewCombinedProvider(cfg, store, nil, t.TempDir())
	if _, err := provider.Get("dynamic"); err == nil {
		t.Fatalf("expected error for missing integration")
	}
}
