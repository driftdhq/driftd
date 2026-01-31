# Driftd

A self-hosted Terraform/Terragrunt drift detection server with a web UI.

## Features

- Detects infrastructure drift by running `terraform plan` or `terragrunt plan`
- Web UI to view drift status across repositories and stacks
- Job queue with Redis for reliable, distributed execution
- Scheduled scans via cron expressions
- Manual scan triggers via UI or API
- Post-apply webhook support for real-time drift status updates

## Architecture

```
┌─────────────┐      ┌─────────────┐      ┌─────────────┐
│   Web UI    │      │    Redis    │      │   Storage   │
│  (serve)    │◄────►│  (queue +   │◄────►│  (PVC/EFS)  │
└─────────────┘      │   locks)    │      └─────────────┘
                     └──────▲──────┘             ▲
                            │                    │
              ┌─────────────┼─────────────┐      │
              │             │             │      │
        ┌─────▼───┐   ┌─────▼───┐   ┌─────▼───┐  │
        │ Worker  │   │ Worker  │   │ Worker  │──┘
        │   (1)   │   │   (2)   │   │   (n)   │
        └─────────┘   └─────────┘   └─────────┘
```

## Installation

```bash
go install github.com/cbrown132/driftd/cmd/driftd@latest
```

Or build from source:

```bash
git clone https://github.com/cbrown132/driftd.git
cd driftd
go build -o driftd ./cmd/driftd
```

## Requirements

- Go 1.21+
- Redis
- Terraform and/or Terragrunt installed on worker nodes
- Git

## Configuration

Create a `config.yaml` file:

```yaml
data_dir: ./data
listen_addr: ":8080"

redis:
  addr: "localhost:6379"
  password: ""
  db: 0

worker:
  concurrency: 5
  lock_ttl: 30m
  retry_once: true

repos:
  - name: my-infra
    url: https://github.com/myorg/terraform-infra.git
    schedule: "0 */6 * * *"  # every 6 hours (optional)
    stacks:
      - envs/prod
      - envs/staging
```

## Usage

Start the web server:

```bash
./driftd serve -config config.yaml
```

Start a worker (can run multiple):

```bash
./driftd worker -config config.yaml
```

Then open http://localhost:8080 in your browser.

## API

| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | Dashboard (HTML) |
| GET | `/repos/{repo}` | Repo detail with stack list (HTML) |
| GET | `/repos/{repo}/stacks/{stack...}` | Stack drift detail (HTML) |
| GET | `/api/health` | Health check |
| GET | `/api/jobs/{jobID}` | Get job status |
| GET | `/api/repos/{repo}/jobs` | List jobs for repo |
| POST | `/api/repos/{repo}/scan` | Scan all stacks in repo |
| POST | `/api/repos/{repo}/stacks/{stack...}/scan` | Scan single stack |

### Triggering Scans

Manual scan via API:

```bash
curl -X POST http://localhost:8080/api/repos/my-infra/scan
```

Post-apply webhook (e.g., from CI):

```bash
curl -X POST http://localhost:8080/api/repos/my-infra/stacks/envs/prod/scan \
  -H "Content-Type: application/json" \
  -d '{"trigger": "post-apply", "commit": "abc123", "actor": "ci"}'
```

## How It Works

1. Configure repositories and stack paths in `config.yaml`
2. The scheduler enqueues jobs based on cron schedules
3. Workers pick up jobs from Redis, acquire repo locks, and run plans
4. Results are saved to filesystem storage and displayed in the UI
5. Repo-level locking prevents concurrent scans of the same repo

Driftd detects Terragrunt by looking for `terragrunt.hcl` in the stack directory.

## Kubernetes Deployment

Driftd is designed for Kubernetes deployment:

- **Server**: Single replica, runs scheduler + web UI
- **Workers**: Scale horizontally based on workload
- **Redis**: Use managed Redis or deploy your own
- **Storage**: Mount a PVC (EBS, EFS, etc.) at `/data`

Helm chart coming soon.

## License

MIT
