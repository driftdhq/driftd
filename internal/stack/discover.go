package stack

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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

func Discover(projectDir, rootPath string, ignore []string) ([]string, error) {
	patterns := append([]string{}, defaultIgnore...)
	patterns = append(patterns, ignore...)
	scopeRoot := ""
	walkRoot := projectDir
	if rootPath != "" {
		if filepath.IsAbs(rootPath) {
			return nil, fmt.Errorf("root path must be relative: %q", rootPath)
		}
		clean := filepath.Clean(rootPath)
		if clean == "." {
			return nil, fmt.Errorf("root path must not be '.'")
		}
		if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return nil, fmt.Errorf("root path must not traverse outside repository: %q", rootPath)
		}
		scopeRoot = filepath.ToSlash(clean)
		walkRoot = filepath.Join(projectDir, clean)
		info, err := os.Stat(walkRoot)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("root path does not exist: %q", scopeRoot)
			}
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("root path is not a directory: %q", scopeRoot)
		}
	}

	seenTG := map[string]struct{}{}
	seenTF := map[string]struct{}{}
	var terragruntStacks []string
	var terraformStacks []string
	rootHasTerragrunt := false

	err := filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(projectDir, path)
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
		normalizedDir := dir
		if normalizedDir == "." {
			normalizedDir = ""
		}
		base := filepath.Base(rel)
		if base == "terragrunt.hcl" {
			if normalizedDir == scopeRoot {
				rootHasTerragrunt = true
			}
			addStack(dir, seenTG, &terragruntStacks)
			return nil
		}
		if strings.HasSuffix(base, ".tf") {
			addStack(dir, seenTF, &terraformStacks)
			return nil
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if rootHasTerragrunt && len(terragruntStacks) > 0 {
		sort.Strings(terragruntStacks)
		return filterParentStacks(terragruntStacks), nil
	}
	all := append(terragruntStacks, terraformStacks...)
	sort.Strings(all)
	return filterParentStacks(all), nil
}

func filterParentStacks(stacks []string) []string {
	if len(stacks) < 2 {
		return stacks
	}
	var filtered []string
	for i, stack := range stacks {
		prefix := stack
		if prefix != "" {
			prefix += "/"
		}
		hasChild := false
		for j := i + 1; j < len(stacks); j++ {
			if strings.HasPrefix(stacks[j], prefix) {
				hasChild = true
				break
			}
		}
		if !hasChild {
			filtered = append(filtered, stack)
		}
	}
	return filtered
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
