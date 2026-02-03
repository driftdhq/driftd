package api

import (
	"fmt"
	"html"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/secrets"
)

var repoNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func containsStack(target string, stacks []string) bool {
	for _, s := range stacks {
		if s == target {
			return true
		}
	}
	return false
}

func isValidRepoName(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	return repoNamePattern.MatchString(name)
}

func isSafeStackPath(stackPath string) bool {
	if stackPath == "" {
		return true
	}
	if filepath.IsAbs(stackPath) {
		return false
	}
	clean := filepath.Clean(stackPath)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}

func (s *Server) sanitizeErrorMessage(msg string) string {
	if s.cfg != nil && s.cfg.DataDir != "" {
		msg = strings.ReplaceAll(msg, s.cfg.DataDir, "<data-dir>")
	}
	tmp := os.TempDir()
	if tmp != "" {
		msg = strings.ReplaceAll(msg, tmp, "<tmp>")
	}
	return msg
}

func (s *Server) getRepoConfig(name string) (*config.RepoConfig, error) {
	if s.repoProvider != nil {
		return s.repoProvider.Get(name)
	}
	if repo := s.cfg.GetRepo(name); repo != nil {
		return repo, nil
	}
	return nil, secrets.ErrRepoNotFound
}

func (s *Server) listConfiguredRepos() []config.RepoConfig {
	repos := make([]config.RepoConfig, 0, len(s.cfg.Repos))
	seen := make(map[string]struct{}, len(s.cfg.Repos))

	for _, repo := range s.cfg.Repos {
		repos = append(repos, repo)
		seen[repo.Name] = struct{}{}
	}

	if s.repoStore == nil {
		return repos
	}

	for _, entry := range s.repoStore.List() {
		if _, ok := seen[entry.Name]; ok {
			continue
		}
		cancel := entry.CancelInflightOnNewTrigger
		repo := config.RepoConfig{
			Name:                       entry.Name,
			URL:                        entry.URL,
			Branch:                     entry.Branch,
			IgnorePaths:                entry.IgnorePaths,
			Schedule:                   entry.Schedule,
			CancelInflightOnNewTrigger: &cancel,
		}
		if entry.Git.Type != "" {
			repo.Git = &config.GitAuthConfig{Type: entry.Git.Type}
			if entry.Git.GitHubApp != nil {
				repo.Git.GitHubApp = &config.GitHubAppConfig{
					AppID:          entry.Git.GitHubApp.AppID,
					InstallationID: entry.Git.GitHubApp.InstallationID,
				}
			}
		}
		repos = append(repos, repo)
	}

	return repos
}

func formatPlanOutput(plan string) template.HTML {
	if plan == "" {
		return ""
	}
	clean := ansiEscapePattern.ReplaceAllString(plan, "")
	lines := strings.Split(clean, "\n")
	var b strings.Builder
	for i, line := range lines {
		class := planLineClass(line)
		escaped := html.EscapeString(line)
		if class != "" {
			b.WriteString(`<span class="plan-line `)
			b.WriteString(class)
			b.WriteString(`">`)
			b.WriteString(escaped)
			b.WriteString(`</span>`)
		} else {
			b.WriteString(escaped)
		}
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	return template.HTML(b.String())
}

func planLineClass(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return ""
	}
	switch trimmed[0] {
	case '+':
		if strings.HasPrefix(trimmed, "+++") {
			return ""
		}
		return "plan-add"
	case '-':
		if strings.HasPrefix(trimmed, "---") {
			return ""
		}
		return "plan-remove"
	case '~':
		return "plan-change"
	default:
		return ""
	}
}

func commitURL(repoURL, sha string) string {
	if repoURL == "" || sha == "" {
		return ""
	}
	clean := strings.TrimSuffix(repoURL, ".git")
	switch {
	case strings.HasPrefix(clean, "git@github.com:"):
		clean = strings.TrimPrefix(clean, "git@github.com:")
		return "https://github.com/" + clean + "/commit/" + sha
	case strings.HasPrefix(clean, "git@gitlab.com:"):
		clean = strings.TrimPrefix(clean, "git@gitlab.com:")
		return "https://gitlab.com/" + clean + "/-/commit/" + sha
	case strings.HasPrefix(clean, "https://github.com/"):
		return clean + "/commit/" + sha
	case strings.HasPrefix(clean, "http://github.com/"):
		return strings.Replace(clean, "http://github.com/", "https://github.com/", 1) + "/commit/" + sha
	case strings.HasPrefix(clean, "https://gitlab.com/"):
		return clean + "/-/commit/" + sha
	case strings.HasPrefix(clean, "http://gitlab.com/"):
		return strings.Replace(clean, "http://gitlab.com/", "https://gitlab.com/", 1) + "/-/commit/" + sha
	default:
		return ""
	}
}

func timeAgo(t time.Time) string {
	if t.IsZero() {
		return "never"
	}

	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", h)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
}
