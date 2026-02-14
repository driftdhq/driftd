package storage

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/driftdhq/driftd/internal/pathutil"
)

type Storage struct {
	dataDir string
}

type Store interface {
	SaveResult(projectName, stackPath string, result *RunResult) error
	GetResult(projectName, stackPath string) (*RunResult, error)
	ListRepos() ([]ProjectStatus, error)
	ListStacks(projectName string) ([]StackStatus, error)
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

type ProjectStatus struct {
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

var (
	ErrInvalidProjectName = errors.New("invalid project name")
	ErrInvalidStackPath   = errors.New("invalid stack path")
	projectNamePattern    = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

func New(dataDir string) *Storage {
	return &Storage{dataDir: dataDir}
}

func (s *Storage) resultsDir() string {
	return filepath.Join(s.dataDir, "results")
}

func (s *Storage) stackDir(baseDir, projectName, stackPath string) string {
	return filepath.Join(baseDir, projectName, safePath(stackPath))
}

func safePath(path string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(path))
}

func (s *Storage) SaveResult(projectName, stackPath string, result *RunResult) error {
	if err := validateProjectName(projectName); err != nil {
		return err
	}
	if err := validateStackPath(stackPath); err != nil {
		return err
	}

	dir := s.stackDir(s.resultsDir(), projectName, stackPath)
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

func (s *Storage) GetResult(projectName, stackPath string) (*RunResult, error) {
	if err := validateProjectName(projectName); err != nil {
		return nil, err
	}
	if err := validateStackPath(stackPath); err != nil {
		return nil, err
	}

	// Prefer the new layout under <data_dir>/results, but support legacy reads
	// from <data_dir>/<project>/<stack> for existing installations.
	stackRelDir := filepath.Join(projectName, safePath(stackPath))
	statusRelPath := filepath.Join(stackRelDir, "status.json")

	baseDir := s.resultsDir()
	statusData, err := readFileUnder(baseDir, statusRelPath)
	if err != nil {
		legacyData, legacyErr := readFileUnder(s.dataDir, statusRelPath)
		if legacyErr != nil {
			return nil, err
		}
		baseDir = s.dataDir
		statusData = legacyData
	}

	var result RunResult
	if err := json.Unmarshal(statusData, &result); err != nil {
		return nil, err
	}

	planRelPath := filepath.Join(stackRelDir, "plan.txt")
	planData, err := readFileUnder(baseDir, planRelPath)
	if err == nil {
		result.PlanOutput = string(planData)
	}

	return &result, nil
}

func (s *Storage) ListRepos() ([]ProjectStatus, error) {
	// Prefer projects under results/, but also include legacy projects for upgrades.
	projectNames := map[string]struct{}{}
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
			if isReservedProjectDir(name) {
				continue
			}
			projectNames[name] = struct{}{}
		}
	}

	if len(projectNames) == 0 {
		return nil, nil
	}

	var projects []ProjectStatus
	for name := range projectNames {
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
		projects = append(projects, ProjectStatus{
			Name:          name,
			Drifted:       driftedCount > 0,
			Stacks:        len(stacks),
			DriftedStacks: driftedCount,
		})
	}
	return projects, nil
}

func (s *Storage) ListStacks(projectName string) ([]StackStatus, error) {
	if err := validateProjectName(projectName); err != nil {
		return nil, err
	}

	merged := map[string]StackStatus{}

	// Load legacy first, then results/ overwrites.
	for _, base := range []string{s.dataDir, s.resultsDir()} {
		entries, err := readDirUnder(base, projectName)
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
			if err := validateStackPath(stackPath); err != nil {
				continue
			}
			result, err := s.GetResult(projectName, stackPath)
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

func isReservedProjectDir(name string) bool {
	switch name {
	case "workspaces", "results":
		return true
	default:
		return false
	}
}

func validateProjectName(name string) error {
	if name == "" || len(name) > 255 || !projectNamePattern.MatchString(name) {
		return ErrInvalidProjectName
	}
	return nil
}

func validateStackPath(stackPath string) error {
	if !pathutil.IsSafeStackPath(stackPath) {
		return ErrInvalidStackPath
	}
	return nil
}

func readFileUnder(baseDir, fileName string) ([]byte, error) {
	root, err := os.OpenRoot(baseDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	return root.ReadFile(fileName)
}

func readDirUnder(baseDir, dirName string) ([]os.DirEntry, error) {
	root, err := os.OpenRoot(baseDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	dir, err := root.Open(dirName)
	if err != nil {
		return nil, err
	}
	defer dir.Close()

	return dir.ReadDir(-1)
}
