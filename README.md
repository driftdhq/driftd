# Driftd

A self-hosted Terraform/Terragrunt drift detection server with a web UI.

## Features

- **Drift Detection**: Runs `terraform plan` or `terragrunt plan` to detect infrastructure drift
- **Web UI**: Dashboard showing drift status across repositories and stacks
- **Task Runs**: Per-repo task runs that fan out to parallel stack jobs
- **Job Queue**: Redis-backed queue with repo-level locking for reliable execution
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
│  (UI/API)   │      │  tasks +    │      └─────────────┘
│             │      │  queue +    │
│             │      │  locks)     │
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
- **Redis**: Task state, job queue, and repo locks (prevents concurrent scans of same repo)
- **Storage**: Filesystem-based, mount a PVC for persistence

## Concepts

- **Repo**: A git repository containing multiple Terraform or Terragrunt stacks.
- **Task run**: A single scan of a repo. Only one task run is active per repo at a time.
- **Job**: A single stack plan within a task run. Jobs can run in parallel across workers.

## Persistence

- **Redis**: Ephemeral task + job state, queue, and locks. If Redis is wiped, you lose in-flight progress but can re-run tasks.
- **Filesystem (`data_dir`)**: Durable plan outputs and drift status for the UI.

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

- **Redis**: For task state, job queue, and distributed locking
- **Storage**: Filesystem path for plan outputs (PVC in Kubernetes)
- **Terraform/Terragrunt**: Auto-installed via tfswitch/tgswitch in container, or pre-installed for local use
- **Git access**: Standard git auth (SSH keys or HTTPS tokens) for private repos

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
  lock_ttl: 30m       # repo lock timeout (minimum 2m)
  retry_once: true    # retry failed jobs once
  task_max_age: 6h    # max time a repo task may run before it's marked failed
  renew_every: 10s    # lock renewal interval (0 = lock_ttl/3, minimum 10s, must be <= lock_ttl/2)

repos:
  - name: my-infra
    url: https://github.com/myorg/terraform-infra.git
    cancel_inflight_on_new_trigger: true
    git:
      type: https
      https_token_env: GIT_TOKEN
      https_username: x-access-token
    schedule: "0 */6 * * *"  # every 6 hours (optional, omit to disable)
    stacks:
      - envs/prod
      - envs/staging
      - envs/dev
```

### Git Authentication (Server + Workers)

Driftd can authenticate to private repos using SSH, HTTPS tokens, or GitHub App credentials. Configure per repo:

**SSH**

```yaml
git:
  type: ssh
  ssh_key_path: /etc/driftd/ssh/id_ed25519
  ssh_known_hosts_path: /etc/driftd/ssh/known_hosts
```

**HTTPS token**

```yaml
git:
  type: https
  https_token_env: GIT_TOKEN
  https_username: x-access-token
```

**GitHub App**

```yaml
git:
  type: github_app
  github_app:
    app_id: 123456
    installation_id: 12345678
    private_key_path: /etc/driftd/github-app.pem
```

Note: GitHub App tokens are short-lived and read-only if you scope the app permissions appropriately.

### Version Detection

Driftd uses [tfswitch](https://tfswitch.warrensbox.com/) and [tgswitch](https://github.com/warrensbox/tgswitch) to automatically detect and install the correct terraform/terragrunt versions based on:

- `.terraform-version` file
- `required_version` in terraform configuration
- `.terragrunt-version` file

If no version is specified, it defaults to the latest version.

### Private Repositories

Driftd relies on standard git authentication. For private repos, provide credentials via:

- SSH keys mounted into the container (e.g., `/home/driftd/.ssh`), or
- HTTPS tokens embedded in the repo URL.

The exact setup depends on your environment and how you already authenticate git.

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
| GET | `/api/tasks/{taskID}` | Get task run status + progress |
| GET | `/api/repos/{repo}/jobs` | List recent jobs for a repository |
| POST | `/api/repos/{repo}/scan` | Trigger scan for all stacks in repo |
| POST | `/api/repos/{repo}/stacks/{stack...}/scan` | Trigger scan for a single stack |

### Conflict Behavior

If a scan is already running for a repo, the API returns `409 Conflict` with the active task and current progress.

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

**Response (task + jobs):**

```json
{
  "jobs": ["my-infra:envs/prod:1706712345678"],
  "task": {
    "id": "my-infra:1706712345678",
    "repo_name": "my-infra",
    "status": "running",
    "total": 12,
    "queued": 11,
    "running": 1,
    "completed": 0,
    "failed": 0,
    "drifted": 0,
    "errored": 0
  },
  "message": "Job enqueued"
}
```

**Response (409 conflict):**

```json
{
  "error": "Repository scan already in progress",
  "active_task": {
    "id": "my-infra:1706712000000",
    "repo_name": "my-infra",
    "status": "running",
    "total": 500,
    "queued": 320,
    "running": 40,
    "completed": 140,
    "failed": 0,
    "drifted": 12,
    "errored": 0
  }
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

- **Repo-level locking**: Only one task run can be active per repository
- **Parallelism by stack**: A task run fans out to one job per stack
- **Worker concurrency**: Each worker can process multiple jobs in parallel (configurable)
- **Lock TTL**: Safety timeout if a worker crashes mid-run (default 30 minutes)

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
2. **Task Creation**: A scan trigger (cron or API) creates a repo-level task run
3. **Queueing**: The task enqueues one job per stack into Redis
4. **Processing**: Workers pull jobs, clone repos, switch versions, and run plans
5. **Storage**: Results (drift status + plan output) are saved to filesystem
6. **Display**: Web UI reads from storage and shows drift status

## Task & Job Lifecycle

```
task created → running → completed
                    ↘→ failed (if any job fails permanently)
```

```
created → pending → running → completed
                          ↘→ failed (retry once, then permanent)
```

Jobs are retained in Redis for 7 days for debugging purposes.
