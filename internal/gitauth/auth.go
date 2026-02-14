package gitauth

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func AuthMethod(ctx context.Context, project *config.ProjectConfig) (transport.AuthMethod, error) {
	if project == nil || project.Git == nil || project.Git.Type == "" {
		return nil, nil
	}

	switch project.Git.Type {
	case "ssh":
		return sshAuth(project.Git)
	case "https":
		return httpsAuth(project.Git)
	case "github_app":
		return githubAppAuth(ctx, project.Git)
	default:
		return nil, fmt.Errorf("unsupported git auth type: %s", project.Git.Type)
	}
}

func sshAuth(cfg *config.GitAuthConfig) (transport.AuthMethod, error) {
	keyPath := cfg.SSHKeyPath
	if keyPath == "" && cfg.SSHKeyEnv != "" {
		keyPath = os.Getenv(cfg.SSHKeyEnv)
	}
	if keyPath == "" {
		return nil, fmt.Errorf("ssh_key_path or ssh_key_env required")
	}

	passphrase := ""
	if cfg.SSHKeyPassphraseEnv != "" {
		passphrase = os.Getenv(cfg.SSHKeyPassphraseEnv)
	}

	auth, err := gitssh.NewPublicKeysFromFile("git", keyPath, passphrase)
	if err != nil {
		return nil, fmt.Errorf("load SSH key from %s: %w", keyPath, err)
	}

	if cfg.SSHInsecureIgnoreHostKey {
		log.Printf("warning: ssh_insecure_ignore_host_key is enabled; host key verification is disabled")
		auth.HostKeyCallback = ssh.InsecureIgnoreHostKey()
		return auth, nil
	}
	if cfg.SSHKnownHostsPath != "" {
		cb, err := knownhosts.New(cfg.SSHKnownHostsPath)
		if err != nil {
			return nil, fmt.Errorf("load ssh known_hosts from %s: %w", cfg.SSHKnownHostsPath, err)
		}
		auth.HostKeyCallback = cb
		return auth, nil
	}

	return nil, fmt.Errorf("ssh_known_hosts_path required unless ssh_insecure_ignore_host_key is true")
}

func httpsAuth(cfg *config.GitAuthConfig) (transport.AuthMethod, error) {
	token := cfg.HTTPSToken
	if token == "" && cfg.HTTPSTokenEnv != "" {
		token = os.Getenv(cfg.HTTPSTokenEnv)
	}
	if token == "" {
		return nil, fmt.Errorf("https_token or https_token_env required")
	}

	return httpsAuthWithToken(cfg, token), nil
}

func httpsAuthWithToken(cfg *config.GitAuthConfig, token string) transport.AuthMethod {
	username := cfg.HTTPSUsername
	if username == "" {
		username = "x-access-token"
	}

	return &githttp.BasicAuth{
		Username: username,
		Password: token,
	}
}
