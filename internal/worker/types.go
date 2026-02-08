package worker

import (
	"github.com/driftdhq/driftd/internal/queue"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

type ScanContext struct {
	RepoName      string
	RepoURL       string
	StackPath     string
	ScanID        string
	CommitSHA     string
	WorkspacePath string
	TFVersion     string
	TGVersion     string
	Auth          transport.AuthMethod
	Scan          *queue.Scan
}
