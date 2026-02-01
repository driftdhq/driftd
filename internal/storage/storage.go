package storage

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type Storage struct {
	dataDir string
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
	Name    string
	Drifted bool
	Stacks  int
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

func (s *Storage) stackDir(repoName, stackPath string) string {
	return filepath.Join(s.dataDir, repoName, safePath(stackPath))
}

func safePath(path string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(path))
}

func (s *Storage) SaveResult(repoName, stackPath string, result *RunResult) error {
	dir := s.stackDir(repoName, stackPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	statusPath := filepath.Join(dir, "status.json")
	statusData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(statusPath, statusData, 0600); err != nil {
		return err
	}

	planPath := filepath.Join(dir, "plan.txt")
	if err := os.WriteFile(planPath, []byte(result.PlanOutput), 0600); err != nil {
		return err
	}

	return nil
}

func (s *Storage) GetResult(repoName, stackPath string) (*RunResult, error) {
	dir := s.stackDir(repoName, stackPath)

	statusPath := filepath.Join(dir, "status.json")
	statusData, err := os.ReadFile(statusPath)
	if err != nil {
		return nil, err
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
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var repos []RepoStatus
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		stacks, err := s.ListStacks(entry.Name())
		if err != nil {
			continue
		}

		drifted := false
		for _, stack := range stacks {
			if stack.Drifted {
				drifted = true
				break
			}
		}

		repos = append(repos, RepoStatus{
			Name:    entry.Name(),
			Drifted: drifted,
			Stacks:  len(stacks),
		})
	}

	return repos, nil
}

func (s *Storage) ListStacks(repoName string) ([]StackStatus, error) {
	repoDir := filepath.Join(s.dataDir, repoName)
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var stacks []StackStatus
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

		stacks = append(stacks, StackStatus{
			Path:      stackPath,
			Drifted:   result.Drifted,
			Added:     result.Added,
			Changed:   result.Changed,
			Destroyed: result.Destroyed,
			Error:     result.Error,
			RunAt:     result.RunAt,
		})
	}

	return stacks, nil
}

func decodeSafePath(value string) (string, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
