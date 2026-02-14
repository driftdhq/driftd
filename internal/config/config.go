package config

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	DataDir    string `yaml:"data_dir"`
	ListenAddr string `yaml:"listen_addr"`
	// InsecureDevMode relaxes auth and secret-key requirements for local-only development.
	// Never enable this in shared or production environments.
	InsecureDevMode bool            `yaml:"insecure_dev_mode"`
	Redis           RedisConfig     `yaml:"redis"`
	Worker          WorkerConfig    `yaml:"worker"`
	Workspace       WorkspaceConfig `yaml:"workspace"`
	Projects        []ProjectConfig `yaml:"projects"`
	Webhook         WebhookConfig   `yaml:"webhook"`
	UIAuth          UIAuthConfig    `yaml:"ui_auth"`
	APIAuth         APIAuthConfig   `yaml:"api_auth"`
	API             APIConfig       `yaml:"api"`
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
	ScanMaxAge  time.Duration `yaml:"scan_max_age"`
	RenewEvery  time.Duration `yaml:"renew_every"`
	// StackTimeout caps how long a single stack plan is allowed to run in a worker.
	StackTimeout time.Duration `yaml:"stack_timeout"`
}

type WorkspaceConfig struct {
	Retention        int   `yaml:"retention"`          // number of workspace snapshots to keep per project
	CleanupAfterPlan *bool `yaml:"cleanup_after_plan"` // remove terraform/terragrunt artifacts from scan workspaces
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
	Username         string `yaml:"username"`
	Password         string `yaml:"password"`
	Token            string `yaml:"token"`
	TokenHeader      string `yaml:"token_header"`
	WriteToken       string `yaml:"write_token"`
	WriteTokenHeader string `yaml:"write_token_header"`
}

type APIConfig struct {
	RateLimitPerMinute int `yaml:"rate_limit_per_minute"`
	// TrustProxy enables honoring X-Forwarded-For / X-Real-IP without checking the
	// direct peer IP. Prefer leaving this false and relying on private/loopback
	// proxy checks.
	TrustProxy bool `yaml:"trust_proxy"`
}

const (
	minLockTTL    = 2 * time.Minute
	minRenewEvery = 10 * time.Second
)

var projectNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type MonorepoProjectConfig struct {
	Name        string   `yaml:"name"`
	Path        string   `yaml:"path"`
	Schedule    string   `yaml:"schedule,omitempty"`
	IgnorePaths []string `yaml:"ignore_paths,omitempty"`
}

type ProjectConfig struct {
	Name                       string                  `yaml:"name"`
	URL                        string                  `yaml:"url"`
	Branch                     string                  `yaml:"branch"`
	IgnorePaths                []string                `yaml:"ignore_paths"`
	Schedule                   string                  `yaml:"schedule"` // cron expression, empty = no scheduled scans
	CancelInflightOnNewTrigger *bool                   `yaml:"cancel_inflight_on_new_trigger"`
	Git                        *GitAuthConfig          `yaml:"git"`
	Projects                   []MonorepoProjectConfig `yaml:"projects,omitempty"`

	// Derived fields used internally after config load/expansion.
	RootPath string `yaml:"-"`
	CloneURL string `yaml:"-"`
}

func (r *ProjectConfig) CancelInflightEnabled() bool {
	if r == nil || r.CancelInflightOnNewTrigger == nil {
		return true
	}
	return *r.CancelInflightOnNewTrigger
}

func (r *ProjectConfig) EffectiveCloneURL() string {
	if r == nil {
		return ""
	}
	if r.CloneURL != "" {
		return r.CloneURL
	}
	return r.URL
}

func (w WorkspaceConfig) CleanupAfterPlanEnabled() bool {
	if w.CleanupAfterPlan == nil {
		return true
	}
	return *w.CleanupAfterPlan
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
			Concurrency:  5,
			LockTTL:      30 * time.Minute,
			RetryOnce:    true,
			ScanMaxAge:   6 * time.Hour,
			RenewEvery:   0,
			StackTimeout: 30 * time.Minute,
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

func (c *Config) GetProject(name string) *ProjectConfig {
	for i := range c.Projects {
		if c.Projects[i].Name == name {
			return &c.Projects[i]
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
	if cfg.Worker.ScanMaxAge == 0 {
		cfg.Worker.ScanMaxAge = 6 * time.Hour
	}
	if cfg.Worker.RenewEvery == 0 {
		cfg.Worker.RenewEvery = cfg.Worker.LockTTL / 3
	}
	if cfg.Worker.StackTimeout == 0 {
		cfg.Worker.StackTimeout = 30 * time.Minute
	}
	if cfg.Worker.StackTimeout < time.Second {
		return nil, fmt.Errorf("worker.stack_timeout must be at least 1s")
	}
	if cfg.Workspace.Retention <= 0 {
		cfg.Workspace.Retention = 5
	}
	if cfg.Workspace.CleanupAfterPlan == nil {
		enabled := true
		cfg.Workspace.CleanupAfterPlan = &enabled
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
	if cfg.APIAuth.WriteTokenHeader == "" {
		cfg.APIAuth.WriteTokenHeader = "X-API-Write-Token"
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
	expandedProjects, err := expandMonorepos(cfg.Projects)
	if err != nil {
		return nil, err
	}
	cfg.Projects = expandedProjects

	return cfg, nil
}

func expandMonorepos(projects []ProjectConfig) ([]ProjectConfig, error) {
	expanded := make([]ProjectConfig, 0, len(projects))
	seenNames := make(map[string]struct{}, len(projects))

	for i, project := range projects {
		source := fmt.Sprintf("projects[%d]", i)
		if !isValidProjectName(project.Name) {
			return nil, fmt.Errorf("%s: invalid project name %q", source, project.Name)
		}
		if strings.TrimSpace(project.URL) == "" {
			return nil, fmt.Errorf("%s (%s): url is required", source, project.Name)
		}

		if len(project.Projects) == 0 {
			project.Projects = nil
			project.RootPath = ""
			project.CloneURL = project.URL
			if err := appendExpandedProject(&expanded, seenNames, project, source); err != nil {
				return nil, err
			}
			continue
		}

		projectRepos, err := expandProjectProjects(project, source)
		if err != nil {
			return nil, err
		}
		for _, projectRepo := range projectRepos {
			if err := appendExpandedProject(&expanded, seenNames, projectRepo, source); err != nil {
				return nil, err
			}
		}
	}

	return expanded, nil
}

func appendExpandedProject(expanded *[]ProjectConfig, seen map[string]struct{}, project ProjectConfig, source string) error {
	if _, ok := seen[project.Name]; ok {
		return fmt.Errorf("%s: duplicate project name %q after expansion", source, project.Name)
	}
	seen[project.Name] = struct{}{}
	*expanded = append(*expanded, project)
	return nil
}

func expandProjectProjects(parent ProjectConfig, source string) ([]ProjectConfig, error) {
	cleanPaths := make([]string, 0, len(parent.Projects))
	for idx := range parent.Projects {
		project := parent.Projects[idx]
		if !isValidProjectName(project.Name) {
			return nil, fmt.Errorf("%s (%s): invalid project name %q", source, parent.Name, project.Name)
		}
		cleanPath, err := normalizeProjectPath(project.Path)
		if err != nil {
			return nil, fmt.Errorf("%s (%s): invalid project path %q: %w", source, parent.Name, project.Path, err)
		}
		cleanPaths = append(cleanPaths, cleanPath)
		parent.Projects[idx].Path = cleanPath
	}

	if err := validateNoOverlappingProjectPaths(cleanPaths); err != nil {
		return nil, fmt.Errorf("%s (%s): %w", source, parent.Name, err)
	}

	expanded := make([]ProjectConfig, 0, len(parent.Projects))
	for _, project := range parent.Projects {
		ignorePaths := copyStringSlice(parent.IgnorePaths)
		if project.IgnorePaths != nil {
			ignorePaths = copyStringSlice(project.IgnorePaths)
		}
		schedule := parent.Schedule
		if project.Schedule != "" {
			schedule = project.Schedule
		}

		expanded = append(expanded, ProjectConfig{
			Name:                       project.Name,
			URL:                        parent.URL,
			Branch:                     parent.Branch,
			IgnorePaths:                ignorePaths,
			Schedule:                   schedule,
			CancelInflightOnNewTrigger: copyBoolPtr(parent.CancelInflightOnNewTrigger),
			Git:                        copyGitAuth(parent.Git),
			Projects:                   nil,
			RootPath:                   project.Path,
			CloneURL:                   parent.URL,
		})
	}

	return expanded, nil
}

func normalizeProjectPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("path is required")
	}

	slashPath := filepath.ToSlash(trimmed)
	clean := path.Clean(slashPath)
	if clean == "." {
		return "", fmt.Errorf("path must not be '.'")
	}
	if strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("path must be relative")
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path must not traverse outside repository")
	}
	return clean, nil
}

func validateNoOverlappingProjectPaths(paths []string) error {
	if len(paths) < 2 {
		return nil
	}
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	for i := 1; i < len(sorted); i++ {
		prev := sorted[i-1]
		curr := sorted[i]
		if prev == curr || strings.HasPrefix(curr, prev+"/") {
			return fmt.Errorf("overlapping project paths detected: %q and %q", prev, curr)
		}
	}
	return nil
}

func copyBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func copyStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	copied := make([]string, len(values))
	copy(copied, values)
	return copied
}

func copyGitAuth(cfg *GitAuthConfig) *GitAuthConfig {
	if cfg == nil {
		return nil
	}
	copied := *cfg
	if cfg.GitHubApp != nil {
		app := *cfg.GitHubApp
		copied.GitHubApp = &app
	}
	return &copied
}

func isValidProjectName(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	return projectNamePattern.MatchString(name)
}
