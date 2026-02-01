package version

import (
	"context"
	"fmt"
	"os"

	"github.com/go-git/go-git/v5"
)

func DetectFromRepoURL(ctx context.Context, repoURL string, stacks []string) (*Versions, error) {
	tmpDir, err := os.MkdirTemp("", "driftd-version-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	_, err = git.PlainCloneContext(ctx, tmpDir, false, &git.CloneOptions{
		URL:   repoURL,
		Depth: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("clone repo: %w", err)
	}

	return Detect(tmpDir, stacks)
}
