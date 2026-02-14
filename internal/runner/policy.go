package runner

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	blockCommentPattern            = regexp.MustCompile(`(?s)/\*.*?\*/`)
	lineCommentPattern             = regexp.MustCompile(`(?m)^\s*(#|//).*$`)
	externalDataSourceBlockPattern = regexp.MustCompile(`(?is)\bdata\s+"external"\s+"[^"]+"\s*\{`)
)

func enforceExternalDataSourcePolicy(stackDir string, blockExternalDataSource bool) error {
	if !blockExternalDataSource {
		return nil
	}

	found, filePath, err := detectExternalDataSource(stackDir)
	if err != nil {
		return fmt.Errorf("external data source policy check failed: %w", err)
	}
	if !found {
		return nil
	}

	relPath := filepath.Base(filePath)
	if rel, relErr := filepath.Rel(stackDir, filePath); relErr == nil {
		relPath = rel
	}
	return fmt.Errorf("stack blocked by policy: Terraform data \"external\" detected in %s", relPath)
}

func detectExternalDataSource(stackDir string) (bool, string, error) {
	var detectedFile string
	err := filepath.WalkDir(stackDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".terraform", ".terragrunt-cache":
				return filepath.SkipDir
			}
			return nil
		}
		if !isTerraformConfigFile(path) {
			return nil
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if !externalDataSourceBlockPattern.MatchString(stripComments(string(content))) {
			return nil
		}

		detectedFile = path
		return fs.SkipAll
	})
	if err != nil && err != fs.SkipAll {
		return false, "", err
	}
	if detectedFile == "" {
		return false, "", nil
	}
	return true, detectedFile, nil
}

func isTerraformConfigFile(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".tf") || strings.HasSuffix(lower, ".tf.json")
}

func stripComments(content string) string {
	noBlocks := blockCommentPattern.ReplaceAllString(content, "")
	return lineCommentPattern.ReplaceAllString(noBlocks, "")
}
