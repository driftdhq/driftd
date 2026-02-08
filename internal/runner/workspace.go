package runner

import (
	"context"
	"fmt"
	"os"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// prepareRepoRoot returns the root directory containing the repo and an optional
// cleanup function. When a shared workspace is available (scan-based flow), plans
// run directly in it — no filesystem copy. When no workspace exists (standalone
// stack scan), the repo is cloned into a temp directory.
func (r *Runner) prepareRepoRoot(ctx context.Context, repoURL, workspacePath string, auth transport.AuthMethod) (string, func(), error) {
	if workspacePath != "" {
		return workspacePath, nil, nil
	}

	tmpDir, err := os.MkdirTemp("", "driftd-*")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp dir: %v", err)
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	_, err = git.PlainCloneContext(ctx, tmpDir, false, &git.CloneOptions{
		URL:   repoURL,
		Depth: 1,
		Auth:  auth,
	})
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to clone repo: %v", err)
	}

	return tmpDir, cleanup, nil
}
