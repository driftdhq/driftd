package repos

import (
	"testing"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/secrets"
)

func TestCombinedProviderPrefersStaticConfig(t *testing.T) {
	cfg := &config.Config{
		Repos: []config.RepoConfig{
			{Name: "repo", URL: "https://example.com/static.git"},
		},
	}

	store := secrets.NewRepoStore(t.TempDir(), nil)
	// Add a dynamic repo with the same name to verify static precedence.
	entry := &secrets.RepoEntry{
		Name: "repo",
		URL:  "https://example.com/dynamic.git",
	}
	if err := store.Add(entry, nil); err != nil {
		t.Fatalf("add entry: %v", err)
	}

	provider := NewCombinedProvider(cfg, store, nil, t.TempDir())

	got, err := provider.Get("repo")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.URL != "https://example.com/static.git" {
		t.Fatalf("expected static repo, got %s", got.URL)
	}
}

func TestCombinedProviderListIncludesDynamic(t *testing.T) {
	cfg := &config.Config{
		Repos: []config.RepoConfig{
			{Name: "static", URL: "https://example.com/static.git"},
		},
	}

	store := secrets.NewRepoStore(t.TempDir(), nil)
	entry := &secrets.RepoEntry{
		Name: "dynamic",
		URL:  "https://example.com/dynamic.git",
	}
	if err := store.Add(entry, nil); err != nil {
		t.Fatalf("add entry: %v", err)
	}

	provider := NewCombinedProvider(cfg, store, nil, t.TempDir())
	repos, err := provider.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos))
	}
}

func TestCombinedProviderMissingIntegration(t *testing.T) {
	cfg := &config.Config{}

	store := secrets.NewRepoStore(t.TempDir(), nil)
	entry := &secrets.RepoEntry{
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
