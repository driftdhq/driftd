package api

import (
	"context"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/queue"
)

func (s *Server) startScanWithCancel(ctx context.Context, repoCfg *config.RepoConfig, trigger, commit, actor string) (*queue.Scan, []string, error) {
	return s.orchestrator.StartScan(ctx, repoCfg, trigger, commit, actor)
}
