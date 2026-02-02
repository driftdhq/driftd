package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	DataDir    string          `yaml:"data_dir"`
	ListenAddr string          `yaml:"listen_addr"`
	Redis      RedisConfig     `yaml:"redis"`
	Worker     WorkerConfig    `yaml:"worker"`
	Workspace  WorkspaceConfig `yaml:"workspace"`
	Repos      []RepoConfig    `yaml:"repos"`
	Webhook    WebhookConfig   `yaml:"webhook"`
	UIAuth     UIAuthConfig    `yaml:"ui_auth"`
	APIAuth    APIAuthConfig   `yaml:"api_auth"`
	API        APIConfig       `yaml:"api"`
}

type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type WorkerConfig struct {
	Concurrency int           `yaml:"concurrency"`
	LockTTL     time.Duration `yaml:"lock_ttl"`
	RetryOnce   bool          `yaml:"retry_once"`
	TaskMaxAge  time.Duration `yaml:"task_max_age"`
	RenewEvery  time.Duration `yaml:"renew_every"`
}

type WorkspaceConfig struct {
	Retention int `yaml:"retention"` // number of workspace snapshots to keep per repo
}

type WebhookConfig struct {
	Enabled      bool   `yaml:"enabled"`
	GitHubSecret string `yaml:"github_secret"`
	Token        string `yaml:"token"`
	TokenHeader  string `yaml:"token_header"`
	MaxFiles     int    `yaml:"max_files"`
}

type UIAuthConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type APIAuthConfig struct {
	Username    string `yaml:"username"`
	Password    string `yaml:"password"`
	Token       string `yaml:"token"`
	TokenHeader string `yaml:"token_header"`
}

type APIConfig struct {
	RateLimitPerMinute int `yaml:"rate_limit_per_minute"`
}

const (
	minLockTTL    = 2 * time.Minute
	minRenewEvery = 10 * time.Second
)

type RepoConfig struct {
	Name                       string         `yaml:"name"`
	URL                        string         `yaml:"url"`
	Branch                     string         `yaml:"branch"`
	IgnorePaths                []string       `yaml:"ignore_paths"`
	Schedule                   string         `yaml:"schedule"` // cron expression, empty = no scheduled scans
	CancelInflightOnNewTrigger *bool          `yaml:"cancel_inflight_on_new_trigger"`
	Git                        *GitAuthConfig `yaml:"git"`
}

func (r *RepoConfig) CancelInflightEnabled() bool {
	if r == nil || r.CancelInflightOnNewTrigger == nil {
		return true
	}
	return *r.CancelInflightOnNewTrigger
}

type GitAuthConfig struct {
	Type string `yaml:"type"` // "ssh", "https", "github_app"

	SSHKeyPath          string `yaml:"ssh_key_path"`
	SSHKeyEnv           string `yaml:"ssh_key_env"`
	SSHKeyPassphraseEnv string `yaml:"ssh_key_passphrase_env"`
	SSHKnownHostsPath   string `yaml:"ssh_known_hosts_path"`
	// SSHInsecureIgnoreHostKey disables host key verification.
	// WARNING: This allows man-in-the-middle attacks. Only use for testing
	// or when connecting to hosts with frequently changing keys.
	SSHInsecureIgnoreHostKey bool `yaml:"ssh_insecure_ignore_host_key"`

	HTTPSUsername string `yaml:"https_username"`
	HTTPSToken    string `yaml:"https_token"`
	HTTPSTokenEnv string `yaml:"https_token_env"`

	GitHubApp *GitHubAppConfig `yaml:"github_app"`
}

type GitHubAppConfig struct {
	AppID          int64  `yaml:"app_id"`
	InstallationID int64  `yaml:"installation_id"`
	PrivateKey     string `yaml:"private_key"`
	PrivateKeyPath string `yaml:"private_key_path"`
	PrivateKeyEnv  string `yaml:"private_key_env"`
	APIBaseURL     string `yaml:"api_base_url"`
}

func Load(path string) (*Config, error) {
	cfg := &Config{
		DataDir:    "./data",
		ListenAddr: ":8080",
		Redis: RedisConfig{
			Addr: "localhost:6379",
			DB:   0,
		},
		Worker: WorkerConfig{
			Concurrency: 5,
			LockTTL:     30 * time.Minute,
			RetryOnce:   true,
			TaskMaxAge:  6 * time.Hour,
			RenewEvery:  0,
		},
		Workspace: WorkspaceConfig{
			Retention: 5,
		},
	}

	if path == "" {
		return applyDefaults(cfg)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return applyDefaults(cfg)
		}
		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	return applyDefaults(cfg)
}

func (c *Config) GetRepo(name string) *RepoConfig {
	for i := range c.Repos {
		if c.Repos[i].Name == name {
			return &c.Repos[i]
		}
	}
	return nil
}

func applyDefaults(cfg *Config) (*Config, error) {
	// Apply defaults for unset values
	if cfg.DataDir == "" {
		cfg.DataDir = "./data"
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}
	if cfg.Redis.Addr == "" {
		cfg.Redis.Addr = "localhost:6379"
	}
	if cfg.Worker.Concurrency < 1 {
		cfg.Worker.Concurrency = 5
	}
	if cfg.Worker.LockTTL == 0 {
		cfg.Worker.LockTTL = 30 * time.Minute
	}
	if cfg.Worker.TaskMaxAge == 0 {
		cfg.Worker.TaskMaxAge = 6 * time.Hour
	}
	if cfg.Worker.RenewEvery == 0 {
		cfg.Worker.RenewEvery = cfg.Worker.LockTTL / 3
	}
	if cfg.Workspace.Retention <= 0 {
		cfg.Workspace.Retention = 5
	}
	if cfg.Webhook.TokenHeader == "" {
		cfg.Webhook.TokenHeader = "X-Webhook-Token"
	}
	if !cfg.Webhook.Enabled && (cfg.Webhook.GitHubSecret != "" || cfg.Webhook.Token != "") {
		cfg.Webhook.Enabled = true
	}
	if cfg.APIAuth.TokenHeader == "" {
		cfg.APIAuth.TokenHeader = "X-API-Token"
	}
	if cfg.Webhook.MaxFiles <= 0 {
		cfg.Webhook.MaxFiles = 300
	}
	if cfg.API.RateLimitPerMinute == 0 {
		cfg.API.RateLimitPerMinute = 60
	}
	if cfg.Webhook.Enabled && cfg.Webhook.GitHubSecret == "" && cfg.Webhook.Token == "" {
		return nil, fmt.Errorf("webhook enabled but github_secret and token are empty")
	}
	if cfg.Worker.LockTTL < minLockTTL {
		return nil, fmt.Errorf("worker.lock_ttl must be at least %s", minLockTTL)
	}
	if cfg.Worker.RenewEvery < minRenewEvery {
		return nil, fmt.Errorf("worker.renew_every must be at least %s", minRenewEvery)
	}
	if cfg.Worker.RenewEvery > cfg.Worker.LockTTL/2 {
		return nil, fmt.Errorf("worker.renew_every must be <= lock_ttl/2")
	}

	return cfg, nil
}
