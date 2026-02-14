package api

import "github.com/driftdhq/driftd/internal/queue"

type apiScan struct {
	ID          string `json:"id"`
	ProjectName string `json:"project_name"`
	Trigger     string `json:"trigger,omitempty"`
	Commit      string `json:"commit,omitempty"`
	Actor       string `json:"actor,omitempty"`
	Status      string `json:"status"`
	CreatedAt   int64  `json:"created_at"`
	StartedAt   int64  `json:"started_at"`
	EndedAt     int64  `json:"ended_at,omitempty"`
	Error       string `json:"error,omitempty"`

	CommitSHA string `json:"commit_sha,omitempty"`

	Total     int `json:"total"`
	Queued    int `json:"queued"`
	Running   int `json:"running"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Drifted   int `json:"drifted"`
	Errored   int `json:"errored"`

	TerraformVersion  string            `json:"terraform_version,omitempty"`
	TerragruntVersion string            `json:"terragrunt_version,omitempty"`
	StackTFVersions   map[string]string `json:"stack_tf_versions,omitempty"`
	StackTGVersions   map[string]string `json:"stack_tg_versions,omitempty"`
}

type apiStackScan struct {
	ID          string `json:"id"`
	ScanID      string `json:"scan_id"`
	ProjectName string `json:"project_name"`
	StackPath   string `json:"stack_path"`
	Status      string `json:"status"`
	Retries     int    `json:"retries"`
	MaxRetries  int    `json:"max_retries"`
	CreatedAt   int64  `json:"created_at"`
	StartedAt   int64  `json:"started_at,omitempty"`
	CompletedAt int64  `json:"completed_at,omitempty"`
	Error       string `json:"error,omitempty"`
	Trigger     string `json:"trigger,omitempty"`
	Commit      string `json:"commit,omitempty"`
	Actor       string `json:"actor,omitempty"`
}

func toAPIScan(scan *queue.Scan) *apiScan {
	if scan == nil {
		return nil
	}
	return &apiScan{
		ID:                scan.ID,
		ProjectName:       scan.ProjectName,
		Trigger:           scan.Trigger,
		Commit:            scan.Commit,
		Actor:             scan.Actor,
		Status:            scan.Status,
		CreatedAt:         scan.CreatedAt.Unix(),
		StartedAt:         scan.StartedAt.Unix(),
		EndedAt:           scan.EndedAt.Unix(),
		Error:             scan.Error,
		CommitSHA:         scan.CommitSHA,
		Total:             scan.Total,
		Queued:            scan.Queued,
		Running:           scan.Running,
		Completed:         scan.Completed,
		Failed:            scan.Failed,
		Drifted:           scan.Drifted,
		Errored:           scan.Errored,
		TerraformVersion:  scan.TerraformVersion,
		TerragruntVersion: scan.TerragruntVersion,
		StackTFVersions:   scan.StackTFVersions,
		StackTGVersions:   scan.StackTGVersions,
	}
}

func toAPIStackScan(scan *queue.StackScan) *apiStackScan {
	if scan == nil {
		return nil
	}
	return &apiStackScan{
		ID:          scan.ID,
		ScanID:      scan.ScanID,
		ProjectName: scan.ProjectName,
		StackPath:   scan.StackPath,
		Status:      scan.Status,
		Retries:     scan.Retries,
		MaxRetries:  scan.MaxRetries,
		CreatedAt:   scan.CreatedAt.Unix(),
		StartedAt:   scan.StartedAt.Unix(),
		CompletedAt: scan.CompletedAt.Unix(),
		Error:       scan.Error,
		Trigger:     scan.Trigger,
		Commit:      scan.Commit,
		Actor:       scan.Actor,
	}
}
