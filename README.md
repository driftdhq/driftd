# Driftd

A self-hosted Terraform/Terragrunt drift detection server with a web UI.

## Features

- **Drift Detection**: Runs `terraform plan` or `terragrunt plan` to detect infrastructure drift
- **Web UI**: Dashboard showing drift status across repositories and stacks
- **Job Queue**: Redis-based queue with distributed locking for reliable execution
- **Scheduled Scans**: Cron expressions per repository for automated drift checks
- **Webhook Support**: POST API for triggering scans after applies (real-time drift status)
- **Version Management**: Auto-detects terraform/terragrunt versions via tfswitch/tgswitch
- **Caching**: Shared provider and binary cache to reduce disk usage and download time

## Architecture

Driftd separates the web server from workers for independent scaling:

```
┌─────────────┐      ┌─────────────┐      ┌─────────────┐
│  driftd     │      │    Redis    │      │   Storage   │
│  serve      │◄────►│  (queue +   │◄────►│   (PVC)     │
│  (UI/API)   │      │   locks)    │      └─────────────┘
└─────────────┘      └──────▲──────┘             ▲
                            │                    │
         ┌──────────────────┼──────────────────┐ │
         │                  │                  │ │
   ┌─────▼─────┐     ┌─────▼─────┐     ┌─────▼─────┐
   │  driftd   │     │  driftd   │     │  driftd   │
   │  worker   │     │  worker   │     │  worker   │
   └───────────┘     └───────────┘     └───────────┘
```

- **serve**: Web UI, API, and scheduler (single replica)
- **worker**: Processes scan jobs (scale horizontally based on workload)
- **Redis**: Job queue and distributed locks (prevents concurrent scans of same repo)
- **Storage**: Filesystem-based, mount a PVC for persistence

## Installation

### From Source

```bash
git clone https://github.com/cbrown132/driftd.git
cd driftd
go build -o driftd ./cmd/driftd
```

### Docker

```bash
docker pull ghcr.io/cbrown132/driftd:latest
```

Or build locally:

```bash
docker build -t driftd .
```

## Requirements

- **Redis**: For job queue and distributed locking
- **Storage**: Filesystem path for plan outputs (PVC in Kubernetes)
- **Terraform/Terragrunt**: Auto-installed via tfswitch/tgswitch in container, or pre-installed for local use

## Configuration

Create a `config.yaml`:

```yaml
data_dir: ./data
listen_addr: ":8080"

redis:
  addr: "localhost:6379"
  password: ""
  db: 0

worker:
  concurrency: 5      # parallel jobs per worker process
  lock_ttl: 30m       # repo lock timeout (safety net for crashed workers)
  retry_once: true    # retry failed jobs once

repos:
  - name: my-infra
    url: https://github.com/myorg/terraform-infra.git
    schedule: "0 */6 * * *"  # every 6 hours (optional, omit to disable)
    stacks:
      - envs/prod
      - envs/staging
      - envs/dev
```

### Version Detection

Driftd uses [tfswitch](https://tfswitch.warrensbox.com/) and [tgswitch](https://github.com/warrensbox/tgswitch) to automatically detect and install the correct terraform/terragrunt versions based on:

- `.terraform-version` file
- `required_version` in terraform configuration
- `.terragrunt-version` file

If no version is specified, it defaults to the latest version.

## Usage

### Running Locally

```bash
# Start Redis
docker run -d -p 6379:6379 redis:alpine

# Start the web server (terminal 1)
./driftd serve -config config.yaml

# Start a worker (terminal 2)
./driftd worker -config config.yaml
```

Open http://localhost:8080 in your browser.

### Running with Docker

```bash
# Start Redis
docker run -d --name redis -p 6379:6379 redis:alpine

# Start server
docker run -d --name driftd-serve \
  -p 8080:8080 \
  -v $(pwd)/config.yaml:/etc/driftd/config.yaml \
  -v driftd-data:/data \
  -v driftd-cache:/cache \
  driftd serve -config /etc/driftd/config.yaml

# Start worker
docker run -d --name driftd-worker \
  -v $(pwd)/config.yaml:/etc/driftd/config.yaml \
  -v driftd-data:/data \
  -v driftd-cache:/cache \
  driftd worker -config /etc/driftd/config.yaml
```

## API

### HTML Routes

| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | Dashboard |
| GET | `/repos/{repo}` | Repository detail with stack list |
| GET | `/repos/{repo}/stacks/{stack...}` | Stack drift detail with plan output |

### API Routes

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/health` | Health check (includes Redis connectivity) |
| GET | `/api/jobs/{jobID}` | Get job status |
| GET | `/api/repos/{repo}/jobs` | List recent jobs for a repository |
| POST | `/api/repos/{repo}/scan` | Trigger scan for all stacks in repo |
| POST | `/api/repos/{repo}/stacks/{stack...}/scan` | Trigger scan for a single stack |

### Triggering Scans

**Manual scan via API:**

```bash
curl -X POST http://localhost:8080/api/repos/my-infra/scan
```

**Post-apply webhook** (e.g., from CI after `terraform apply`):

```bash
curl -X POST http://localhost:8080/api/repos/my-infra/stacks/envs/prod/scan \
  -H "Content-Type: application/json" \
  -d '{"trigger": "post-apply", "commit": "abc123", "actor": "ci"}'
```

This keeps drift status accurate in real-time as changes are applied.

**Response:**

```json
{
  "jobs": ["my-infra:envs/prod:1706712345678"],
  "message": "Job enqueued"
}
```

## Caching

The Docker image uses a `/cache` volume for:

```
/cache/
├── terraform/
│   ├── plugins/     # TF_PLUGIN_CACHE_DIR - shared providers across all stacks
│   └── versions/    # tfswitch binary cache
└── terragrunt/
    ├── download/    # TERRAGRUNT_DOWNLOAD - terragrunt module cache
    └── versions/    # tgswitch binary cache
```

Mount this as a persistent volume to:
- Share providers across stacks (significant disk savings)
- Cache terraform/terragrunt binaries (faster version switching)
- Reduce network downloads

## Concurrency & Locking

- **Repo-level locking**: Only one scan can run per repository at a time
- **Worker concurrency**: Each worker can process multiple jobs in parallel (configurable)
- **Lock TTL**: Safety timeout if a worker crashes mid-job (default 30 minutes)

This prevents resource contention and ensures consistent state reads.

## Kubernetes Deployment

Driftd is designed for Kubernetes:

- **Server**: Single replica Deployment (runs scheduler)
- **Workers**: Deployment with HPA based on queue depth
- **Redis**: Use managed Redis (ElastiCache, Memorystore) or deploy your own
- **Storage**: PVC mounted at `/data` (EBS, EFS, etc.)
- **Cache**: PVC mounted at `/cache` (can be shared ReadWriteMany for multi-worker)

Helm chart coming soon.

## How It Works

1. **Configuration**: Define repositories and stacks in `config.yaml`
2. **Scheduling**: The scheduler enqueues jobs based on cron expressions
3. **Queueing**: Jobs are pushed to Redis with repo-level lock checks
4. **Processing**: Workers pull jobs, acquire locks, clone repos, switch versions, run plans
5. **Storage**: Results (drift status + plan output) are saved to filesystem
6. **Display**: Web UI reads from storage and shows drift status

## Job Lifecycle

```
created → pending → running → completed
                          ↘→ failed (retry once, then permanent)
```

Jobs are retained in Redis for 7 days for debugging purposes.

## License

MIT
