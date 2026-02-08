package runner

import (
	"context"
	"fmt"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

func prepareWorkspace(ctx context.Context, tmpDir, repoURL, workspacePath string, auth transport.AuthMethod) error {
	if workspacePath != "" {
		if err := copyRepo(workspacePath, tmpDir); err != nil {
			return fmt.Errorf("failed to copy workspace from %s: %v", workspacePath, err)
		}
		return nil
	}

	_, err := git.PlainCloneContext(ctx, tmpDir, false, &git.CloneOptions{
		URL:   repoURL,
		Depth: 1,
		Auth:  auth,
	})
	if err != nil {
		return fmt.Errorf("failed to clone repo: %v", err)
	}
	return nil
}
