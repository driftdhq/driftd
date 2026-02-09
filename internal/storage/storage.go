package storage

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Storage struct {
	dataDir string
}

type Store interface {
	SaveResult(repoName, stackPath string, result *RunResult) error
	GetResult(repoName, stackPath string) (*RunResult, error)
	ListRepos() ([]RepoStatus, error)
	ListStacks(repoName string) ([]StackStatus, error)
}

type RunResult struct {
	Drifted    bool      `json:"drifted"`
	Added      int       `json:"added"`
	Changed    int       `json:"changed"`
	Destroyed  int       `json:"destroyed"`
	PlanOutput string    `json:"-"`
	Error      string    `json:"error,omitempty"`
	RunAt      time.Time `json:"run_at"`
}

type RepoStatus struct {
	Name          string
	Drifted       bool
	Stacks        int
	DriftedStacks int
}

type StackStatus struct {
	Path      string
	Drifted   bool
	Added     int
	Changed   int
	Destroyed int
	Error     string
	RunAt     time.Time
}

func New(dataDir string) *Storage {
	return &Storage{dataDir: dataDir}
}

func (s *Storage) resultsDir() string {
	return filepath.Join(s.dataDir, "results")
}

func (s *Storage) stackDir(baseDir, repoName, stackPath string) string {
	return filepath.Join(baseDir, repoName, safePath(stackPath))
}

func safePath(path string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(path))
}

func (s *Storage) SaveResult(repoName, stackPath string, result *RunResult) error {
	dir := s.stackDir(s.resultsDir(), repoName, stackPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	statusPath := filepath.Join(dir, "status.json")
	statusData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(statusPath, statusData, 0600); err != nil {
		return err
	}

	planPath := filepath.Join(dir, "plan.txt")
	if err := writeFileAtomic(planPath, []byte(result.PlanOutput), 0600); err != nil {
		return err
	}

	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}

	return nil
}

func (s *Storage) GetResult(repoName, stackPath string) (*RunResult, error) {
	// Prefer the new layout under <data_dir>/results, but support legacy reads
	// from <data_dir>/<repo>/<stack> for existing installations.
	dir := s.stackDir(s.resultsDir(), repoName, stackPath)

	statusPath := filepath.Join(dir, "status.json")
	statusData, err := os.ReadFile(statusPath)
	if err != nil {
		legacyDir := s.stackDir(s.dataDir, repoName, stackPath)
		legacyStatus := filepath.Join(legacyDir, "status.json")
		legacyData, legacyErr := os.ReadFile(legacyStatus)
		if legacyErr != nil {
			return nil, err
		}
		dir = legacyDir
		statusPath = legacyStatus
		statusData = legacyData
	}

	var result RunResult
	if err := json.Unmarshal(statusData, &result); err != nil {
		return nil, err
	}

	planPath := filepath.Join(dir, "plan.txt")
	planData, err := os.ReadFile(planPath)
	if err == nil {
		result.PlanOutput = string(planData)
	}

	return &result, nil
}

func (s *Storage) ListRepos() ([]RepoStatus, error) {
	// Prefer repos under results/, but also include legacy repos for upgrades.
	repoNames := map[string]struct{}{}
	for _, base := range []string{s.resultsDir(), s.dataDir} {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if isReservedRepoDir(name) {
				continue
			}
			repoNames[name] = struct{}{}
		}
	}

	if len(repoNames) == 0 {
		return nil, nil
	}

	var repos []RepoStatus
	for name := range repoNames {
		stacks, err := s.ListStacks(name)
		if err != nil {
			continue
		}
		driftedCount := 0
		for _, stack := range stacks {
			if stack.Drifted {
				driftedCount++
			}
		}
		repos = append(repos, RepoStatus{
			Name:          name,
			Drifted:       driftedCount > 0,
			Stacks:        len(stacks),
			DriftedStacks: driftedCount,
		})
	}
	return repos, nil
}

func (s *Storage) ListStacks(repoName string) ([]StackStatus, error) {
	merged := map[string]StackStatus{}

	// Load legacy first, then results/ overwrites.
	for _, base := range []string{s.dataDir, s.resultsDir()} {
		repoDir := filepath.Join(base, repoName)
		entries, err := os.ReadDir(repoDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			stackPath, err := decodeSafePath(entry.Name())
			if err != nil {
				continue
			}
			result, err := s.GetResult(repoName, stackPath)
			if err != nil {
				continue
			}
			merged[stackPath] = StackStatus{
				Path:      stackPath,
				Drifted:   result.Drifted,
				Added:     result.Added,
				Changed:   result.Changed,
				Destroyed: result.Destroyed,
				Error:     result.Error,
				RunAt:     result.RunAt,
			}
		}
	}

	if len(merged) == 0 {
		return nil, nil
	}
	stacks := make([]StackStatus, 0, len(merged))
	for _, st := range merged {
		stacks = append(stacks, st)
	}
	return stacks, nil
}

func decodeSafePath(value string) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		// Backward compatibility for legacy "__" encoding.
		return strings.ReplaceAll(value, "__", "/"), nil
	}
	return string(data), nil
}

func isReservedRepoDir(name string) bool {
	switch name {
	case "workspaces", "results":
		return true
	default:
		return false
	}
}
