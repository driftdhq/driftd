package api

import (
	"context"

	"github.com/driftdhq/driftd/internal/config"
	"github.com/driftdhq/driftd/internal/queue"
)

func (s *Server) startScanWithCancel(ctx context.Context, projectCfg *config.ProjectConfig, trigger, commit, actor string) (*queue.Scan, []string, error) {
	return s.orchestrator.StartScan(ctx, projectCfg, trigger, commit, actor)
}
