package version

import (
	"context"
	"fmt"
	"os"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

func DetectFromProjectURL(ctx context.Context, projectURL string, stacks []string, auth transport.AuthMethod) (*Versions, error) {
	tmpDir, err := os.MkdirTemp("", "driftd-version-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	_, err = git.PlainCloneContext(ctx, tmpDir, false, &git.CloneOptions{
		URL:   projectURL,
		Depth: 1,
		Auth:  auth,
	})
	if err != nil {
		return nil, fmt.Errorf("clone project: %w", err)
	}

	return Detect(tmpDir, stacks)
}
