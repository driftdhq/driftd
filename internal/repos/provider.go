package repos

import (
	"fmt"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/secrets"
)

// Provider resolves repository configurations from static config and dynamic store.
type Provider interface {
	List() ([]config.RepoConfig, error)
	Get(name string) (*config.RepoConfig, error)
}

// CombinedProvider merges static config repos with dynamic repos.
// Static config takes precedence over dynamic entries.
type CombinedProvider struct {
	cfg     *config.Config
	store   *secrets.RepoStore
	dataDir string
}

func NewCombinedProvider(cfg *config.Config, store *secrets.RepoStore, dataDir string) *CombinedProvider {
	return &CombinedProvider{
		cfg:     cfg,
		store:   store,
		dataDir: dataDir,
	}
}

func (p *CombinedProvider) List() ([]config.RepoConfig, error) {
	repos := make([]config.RepoConfig, 0, len(p.cfg.Repos))
	seen := make(map[string]struct{}, len(p.cfg.Repos))

	for _, repo := range p.cfg.Repos {
		repos = append(repos, repo)
		seen[repo.Name] = struct{}{}
	}

	if p.store == nil {
		return repos, nil
	}

	for _, entry := range p.store.List() {
		if _, ok := seen[entry.Name]; ok {
			continue
		}
		entryWithCreds, creds, err := p.store.GetWithCredentials(entry.Name)
		if err != nil {
			return nil, err
		}
		repoCfg, err := secrets.RepoConfigFromEntry(entryWithCreds, creds, p.dataDir)
		if err != nil {
			return nil, fmt.Errorf("failed to build repo config for %s: %w", entry.Name, err)
		}
		repos = append(repos, *repoCfg)
	}

	return repos, nil
}

func (p *CombinedProvider) Get(name string) (*config.RepoConfig, error) {
	if repo := p.cfg.GetRepo(name); repo != nil {
		return repo, nil
	}
	if p.store == nil {
		return nil, secrets.ErrRepoNotFound
	}
	entry, creds, err := p.store.GetWithCredentials(name)
	if err != nil {
		return nil, err
	}
	return secrets.RepoConfigFromEntry(entry, creds, p.dataDir)
}
