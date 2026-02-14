package api

import (
	"fmt"
	"html"
	"html/template"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/projects"
	"github.com/driftdhq/driftd/internal/secrets"
)

var projectNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func containsStack(target string, stacks []string) bool {
	for _, s := range stacks {
		if s == target {
			return true
		}
	}
	return false
}

func isValidProjectName(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	return projectNamePattern.MatchString(name)
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

func (s *Server) getProjectConfig(name string) (*config.ProjectConfig, error) {
	if s.projectProvider != nil {
		return s.projectProvider.Get(name)
	}
	if project := s.cfg.GetProject(name); project != nil {
		return project, nil
	}
	return nil, secrets.ErrProjectNotFound
}

func (s *Server) listConfiguredRepos() []config.ProjectConfig {
	projects := make([]config.ProjectConfig, 0, len(s.cfg.Projects))
	seen := make(map[string]struct{}, len(s.cfg.Projects))

	for _, project := range s.cfg.Projects {
		projects = append(projects, project)
		seen[project.Name] = struct{}{}
	}

	if s.projectStore == nil {
		return projects
	}

	for _, entry := range s.projectStore.List() {
		if _, ok := seen[entry.Name]; ok {
			continue
		}
		cancel := entry.CancelInflightOnNewTrigger
		project := config.ProjectConfig{
			Name:                       entry.Name,
			URL:                        entry.URL,
			CloneURL:                   entry.URL,
			Branch:                     entry.Branch,
			IgnorePaths:                entry.IgnorePaths,
			Schedule:                   entry.Schedule,
			CancelInflightOnNewTrigger: &cancel,
		}
		if entry.Git.Type != "" {
			project.Git = &config.GitAuthConfig{Type: entry.Git.Type}
			if entry.Git.GitHubApp != nil {
				project.Git.GitHubApp = &config.GitHubAppConfig{
					AppID:          entry.Git.GitHubApp.AppID,
					InstallationID: entry.Git.GitHubApp.InstallationID,
				}
			}
		}
		projects = append(projects, project)
	}

	return projects
}

func (s *Server) getReposByURL(urls ...string) ([]*config.ProjectConfig, error) {
	if len(urls) == 0 {
		return nil, nil
	}

	candidates := make(map[string]struct{}, len(urls))
	for _, rawURL := range urls {
		canonical, ok := projects.CanonicalURL(rawURL)
		if !ok {
			continue
		}
		candidates[canonical] = struct{}{}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	all := s.listConfiguredRepos()
	names := make(map[string]struct{})
	for _, project := range all {
		canonical, ok := projects.CanonicalURL(project.EffectiveCloneURL())
		if !ok {
			continue
		}
		if _, match := candidates[canonical]; !match {
			continue
		}
		names[project.Name] = struct{}{}
	}

	if len(names) == 0 {
		return nil, nil
	}

	sortedNames := make([]string, 0, len(names))
	for name := range names {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)

	matched := make([]*config.ProjectConfig, 0, len(sortedNames))
	for _, name := range sortedNames {
		projectCfg, err := s.getProjectConfig(name)
		if err != nil {
			if err == secrets.ErrProjectNotFound {
				continue
			}
			return nil, err
		}
		if projectCfg != nil {
			matched = append(matched, projectCfg)
		}
	}
	return matched, nil
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

func commitURL(projectURL, sha string) string {
	if projectURL == "" || sha == "" {
		return ""
	}
	clean := strings.TrimSuffix(projectURL, ".git")
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
