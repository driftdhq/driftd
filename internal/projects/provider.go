package projects

import (
	"fmt"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/secrets"
)

// Provider resolves repository configurations from static config and dynamic store.
type Provider interface {
	List() ([]config.ProjectConfig, error)
	Get(name string) (*config.ProjectConfig, error)
}

// CombinedProvider merges static config projects with dynamic projects.
// Static config takes precedence over dynamic entries.
type CombinedProvider struct {
	cfg     *config.Config
	store   *secrets.ProjectStore
	ints    *secrets.IntegrationStore
	dataDir string
}

func NewCombinedProvider(cfg *config.Config, store *secrets.ProjectStore, ints *secrets.IntegrationStore, dataDir string) *CombinedProvider {
	return &CombinedProvider{
		cfg:     cfg,
		store:   store,
		ints:    ints,
		dataDir: dataDir,
	}
}

func (p *CombinedProvider) List() ([]config.ProjectConfig, error) {
	projects := make([]config.ProjectConfig, 0, len(p.cfg.Projects))
	seen := make(map[string]struct{}, len(p.cfg.Projects))

	for _, project := range p.cfg.Projects {
		projects = append(projects, project)
		seen[project.Name] = struct{}{}
	}

	if p.store == nil {
		return projects, nil
	}

	for _, entry := range p.store.List() {
		if _, ok := seen[entry.Name]; ok {
			continue
		}
		entryWithCreds, creds, err := p.store.GetWithCredentials(entry.Name)
		if err != nil {
			return nil, err
		}
		integration, err := p.lookupIntegration(entryWithCreds.IntegrationID)
		if err != nil {
			return nil, err
		}
		projectCfg, err := secrets.ProjectConfigFromEntry(entryWithCreds, creds, integration, p.dataDir)
		if err != nil {
			return nil, fmt.Errorf("failed to build project config for %s: %w", entry.Name, err)
		}
		projects = append(projects, *projectCfg)
	}

	return projects, nil
}

func (p *CombinedProvider) Get(name string) (*config.ProjectConfig, error) {
	if project := p.cfg.GetProject(name); project != nil {
		return project, nil
	}
	if p.store == nil {
		return nil, secrets.ErrProjectNotFound
	}
	entry, creds, err := p.store.GetWithCredentials(name)
	if err != nil {
		return nil, err
	}
	integration, err := p.lookupIntegration(entry.IntegrationID)
	if err != nil {
		return nil, err
	}
	return secrets.ProjectConfigFromEntry(entry, creds, integration, p.dataDir)
}

func (p *CombinedProvider) lookupIntegration(id string) (*secrets.IntegrationEntry, error) {
	if id == "" {
		return nil, nil
	}
	if p.ints == nil {
		return nil, fmt.Errorf("integration store not configured")
	}
	return p.ints.Get(id)
}
