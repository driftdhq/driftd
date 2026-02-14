package secrets

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/driftdhq/driftd/internal/config"
)

// ProjectConfigFromEntry converts a dynamic ProjectEntry and credentials into a ProjectConfig.
func ProjectConfigFromEntry(entry *ProjectEntry, creds *ProjectCredentials, integration *IntegrationEntry, dataDir string) (*config.ProjectConfig, error) {
	if entry == nil {
		return nil, fmt.Errorf("project entry required")
	}

	cfg := &config.ProjectConfig{
		Name:        entry.Name,
		URL:         entry.URL,
		CloneURL:    entry.URL,
		Branch:      entry.Branch,
		IgnorePaths: entry.IgnorePaths,
		Schedule:    entry.Schedule,
	}
	cancel := entry.CancelInflightOnNewTrigger
	cfg.CancelInflightOnNewTrigger = &cancel

	if integration != nil {
		gitCfg, err := gitConfigFromIntegration(entry, integration, dataDir)
		if err != nil {
			return nil, err
		}
		cfg.Git = gitCfg
		return cfg, nil
	}

	if entry.Git.Type == "" {
		return cfg, nil
	}
	if creds == nil {
		return nil, fmt.Errorf("credentials required for git auth type %s", entry.Git.Type)
	}

	gitCfg := &config.GitAuthConfig{Type: entry.Git.Type}

	switch entry.Git.Type {
	case "github_app":
		if entry.Git.GitHubApp == nil {
			return nil, fmt.Errorf("github_app config required")
		}
		if creds.GitHubAppPrivateKey == "" {
			return nil, fmt.Errorf("github_app private key required")
		}
		gitCfg.GitHubApp = &config.GitHubAppConfig{
			AppID:          entry.Git.GitHubApp.AppID,
			InstallationID: entry.Git.GitHubApp.InstallationID,
			PrivateKey:     creds.GitHubAppPrivateKey,
		}
	case "https":
		gitCfg.HTTPSUsername = creds.HTTPSUsername
		gitCfg.HTTPSToken = creds.HTTPSToken
	case "ssh":
		if creds.SSHPrivateKey == "" {
			return nil, fmt.Errorf("ssh private key required")
		}
		keyPath, knownHostsPath, err := writeSSHCredentials(entry.Name, dataDir, creds)
		if err != nil {
			return nil, err
		}
		gitCfg.SSHKeyPath = keyPath
		gitCfg.SSHKnownHostsPath = knownHostsPath
	default:
		return nil, fmt.Errorf("unsupported git auth type: %s", entry.Git.Type)
	}

	cfg.Git = gitCfg
	return cfg, nil
}

func writeSSHCredentials(projectName, dataDir string, creds *ProjectCredentials) (string, string, error) {
	baseDir := filepath.Join(dataDir, "project-creds", projectName)
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return "", "", fmt.Errorf("failed to create credentials dir: %w", err)
	}

	keyPath := filepath.Join(baseDir, "id_ssh")
	if err := os.WriteFile(keyPath, []byte(creds.SSHPrivateKey), 0600); err != nil {
		return "", "", fmt.Errorf("failed to write ssh key: %w", err)
	}

	if creds.SSHKnownHosts == "" {
		return keyPath, "", fmt.Errorf("ssh known_hosts required")
	}

	knownHostsPath := filepath.Join(baseDir, "known_hosts")
	if err := os.WriteFile(knownHostsPath, []byte(creds.SSHKnownHosts), 0600); err != nil {
		return "", "", fmt.Errorf("failed to write known_hosts: %w", err)
	}

	return keyPath, knownHostsPath, nil
}

func gitConfigFromIntegration(entry *ProjectEntry, integration *IntegrationEntry, dataDir string) (*config.GitAuthConfig, error) {
	if integration == nil {
		return nil, fmt.Errorf("integration required")
	}

	gitCfg := &config.GitAuthConfig{Type: integration.Type}

	switch integration.Type {
	case "github_app":
		if integration.GitHubApp == nil {
			return nil, fmt.Errorf("github_app integration config required")
		}
		gitCfg.GitHubApp = &config.GitHubAppConfig{
			AppID:          integration.GitHubApp.AppID,
			InstallationID: integration.GitHubApp.InstallationID,
			PrivateKeyPath: integration.GitHubApp.PrivateKeyPath,
			PrivateKeyEnv:  integration.GitHubApp.PrivateKeyEnv,
			APIBaseURL:     integration.GitHubApp.APIBaseURL,
		}
	case "ssh":
		if integration.SSH == nil {
			return nil, fmt.Errorf("ssh integration config required")
		}
		gitCfg.SSHKeyPath = integration.SSH.KeyPath
		gitCfg.SSHKeyEnv = integration.SSH.KeyEnv
		gitCfg.SSHKeyPassphraseEnv = integration.SSH.KeyPassphraseEnv
		gitCfg.SSHKnownHostsPath = integration.SSH.KnownHostsPath
		gitCfg.SSHInsecureIgnoreHostKey = integration.SSH.InsecureIgnoreHostKey
	case "https":
		if integration.HTTPS == nil {
			return nil, fmt.Errorf("https integration config required")
		}
		gitCfg.HTTPSUsername = integration.HTTPS.Username
		gitCfg.HTTPSTokenEnv = integration.HTTPS.TokenEnv
	default:
		return nil, fmt.Errorf("unsupported integration type: %s", integration.Type)
	}

	return gitCfg, nil
}
