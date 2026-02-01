package stack

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

var defaultIgnore = []string{
	".git/**",
	".terraform/**",
	".terragrunt-cache/**",
	"**/.terraform/**",
	"**/.terragrunt-cache/**",
	"**/vendor/**",
	"**/node_modules/**",
}

func Discover(repoDir string, ignore []string) ([]string, error) {
	patterns := append([]string{}, defaultIgnore...)
	patterns = append(patterns, ignore...)

	seen := map[string]struct{}{}
	var stacks []string

	err := filepath.WalkDir(repoDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repoDir, path)
		if rel == "." {
			return nil
		}

		rel = filepath.ToSlash(rel)
		if shouldIgnore(rel, patterns) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			return nil
		}

		dir := filepath.ToSlash(filepath.Dir(rel))
		base := filepath.Base(rel)
		if base == "terragrunt.hcl" {
			addStack(dir, seen, &stacks)
			return nil
		}
		if strings.HasSuffix(base, ".tf") {
			addStack(dir, seen, &stacks)
			return nil
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stacks, nil
}

func addStack(dir string, seen map[string]struct{}, stacks *[]string) {
	if dir == "." {
		dir = ""
	}
	if _, ok := seen[dir]; ok {
		return
	}
	seen[dir] = struct{}{}
	*stacks = append(*stacks, dir)
}

func shouldIgnore(path string, patterns []string) bool {
	for _, p := range patterns {
		if p == "" {
			continue
		}
		if matchGlob(p, path) {
			return true
		}
	}
	return false
}

func matchGlob(pattern, path string) bool {
	ok, err := doublestar.Match(pattern, path)
	if err == nil && ok {
		return true
	}
	ok, _ = filepath.Match(pattern, path)
	return ok
}
