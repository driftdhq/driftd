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
	CancelInflightOnNewTrigger bool           `yaml:"cancel_inflight_on_new_trigger"`
	Git                        *GitAuthConfig `yaml:"git"`
}

type GitAuthConfig struct {
	Type string `yaml:"type"` // "ssh", "https", "github_app"

	SSHKeyPath               string `yaml:"ssh_key_path"`
	SSHKeyEnv                string `yaml:"ssh_key_env"`
	SSHKeyPassphraseEnv      string `yaml:"ssh_key_passphrase_env"`
	SSHKnownHostsPath        string `yaml:"ssh_known_hosts_path"`
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
	if cfg.Worker.Concurrency == 0 {
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
