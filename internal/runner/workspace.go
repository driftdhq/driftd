package runner

import (
	"context"
	"fmt"
	"os"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// prepareProjectRoot returns the root directory containing the project and an optional
// cleanup function. When a shared workspace is available (scan-based flow), plans
// run directly in it — no filesystem copy. When no workspace exists (standalone
// stack scan), the project is cloned into a temp directory.
func (r *Runner) prepareProjectRoot(ctx context.Context, projectURL, workspacePath string, auth transport.AuthMethod, cloneDepth int) (string, func(), error) {
	if workspacePath != "" {
		return workspacePath, nil, nil
	}

	tmpDir, err := os.MkdirTemp("", "driftd-*")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp dir: %v", err)
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	_, err = git.PlainCloneContext(ctx, tmpDir, false, &git.CloneOptions{
		URL:   projectURL,
		Depth: normalizeCloneDepth(cloneDepth),
		Auth:  auth,
	})
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to clone project: %v", err)
	}

	return tmpDir, cleanup, nil
}

func normalizeCloneDepth(depth int) int {
	if depth <= 0 {
		return 1
	}
	return depth
}
