package repos

import (
	"net/url"
	"path"
	"path/filepath"
	"strings"
)

// CanonicalURL normalizes Git repository URLs for deterministic matching across
// HTTPS/SSH forms. It returns (canonical, true) on success.
func CanonicalURL(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}

	if canonical, ok := canonicalizeScpURL(trimmed); ok {
		return canonical, true
	}

	parsed, err := url.Parse(trimmed)
	if err == nil && parsed.Host != "" {
		host := strings.ToLower(parsed.Host)
		repoPath := normalizeRepoPath(parsed.Path)
		if repoPath == "" {
			return "", false
		}
		return host + "/" + repoPath, true
	}

	cleanPath := filepath.ToSlash(filepath.Clean(trimmed))
	if cleanPath == "" || cleanPath == "." {
		return "", false
	}
	return "local:" + cleanPath, true
}

func canonicalizeScpURL(raw string) (string, bool) {
	if strings.Contains(raw, "://") || !strings.Contains(raw, ":") {
		return "", false
	}
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	hostPart := parts[0]
	repoPath := parts[1]
	if at := strings.LastIndex(hostPart, "@"); at >= 0 {
		hostPart = hostPart[at+1:]
	}
	hostPart = strings.ToLower(strings.TrimSpace(hostPart))
	normalizedPath := normalizeRepoPath(repoPath)
	if hostPart == "" || normalizedPath == "" {
		return "", false
	}
	return hostPart + "/" + normalizedPath, true
}

func normalizeRepoPath(raw string) string {
	p := strings.TrimSpace(filepath.ToSlash(raw))
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return ""
	}
	clean := path.Clean(p)
	if clean == "." || clean == "/" || clean == ".." || strings.HasPrefix(clean, "../") {
		return ""
	}
	return strings.TrimSuffix(clean, ".git")
}
