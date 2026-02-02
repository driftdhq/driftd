# Driftd Helm Chart

This chart deploys the Driftd server and worker components.

## Prerequisites

- Helm v3
- Redis (managed or in-cluster)
- A storage class that supports your desired access mode (RWX recommended for shared data/cache)

## Install

```bash
helm install driftd ./helm/driftd \
  --set image.repository=ghcr.io/driftdhq/driftd \
  --set image.tag=latest
```

## Values

Key values in `values.yaml`:

- `image.repository`, `image.tag`, `image.pullPolicy`
- `service.type`, `service.port`
- `server.replicas`, `server.resources`, `server.envFrom`
- `worker.replicas`, `worker.resources`, `worker.envFrom`
- `storage.data` and `storage.cache` PVC settings
- `config`: the Driftd `config.yaml` rendered into a ConfigMap

## Example Values

```yaml
config:
  data_dir: /data
  redis:
    addr: "redis:6379"
  repos:
    - name: infra
      url: git@github.com:myorg/infra.git
      branch: main
      ignore_paths:
        - "**/modules/**"
      git:
        type: ssh
        ssh_key_path: /etc/driftd/ssh/id_ed25519
        ssh_known_hosts_path: /etc/driftd/ssh/known_hosts

server:
  envFrom:
    - secretRef:
        name: driftd-secrets

worker:
  envFrom:
    - secretRef:
        name: driftd-secrets
```

## Redis

The chart does not install Redis. Configure `config.redis.addr` (and `password` if needed) to point at your Redis service.

## Storage

Driftd uses two PVCs by default:

- `storage.data`: Plan outputs, status files, workspace snapshots
- `storage.cache`: Terraform provider cache and tfswitch/tgswitch binaries

For multi-worker deployments, use `ReadWriteMany` volumes if possible. If only `ReadWriteOnce` is available, keep workers on a single node or reduce worker replicas to 1.

## Credentials

### Git credentials

- **SSH**: Mount keys at the paths referenced in `config.repos[].git`.
- **HTTPS token**: Use an environment variable and reference it via `https_token_env`.
- **GitHub App**: Mount the private key and set `app_id`, `installation_id`, and `private_key_path`.

### Cloud provider credentials (Terraform)

Terraform runs in the worker container and uses whatever credentials you provide. Options include:

- Environment variables (AWS/GCP/Azure standard vars)
- Mounted credential files (AWS shared config, GCP JSON key, Azure profile)
- Kubernetes workload identity (EKS IRSA, GKE Workload Identity, Azure Workload Identity)

The `server.envFrom` and `worker.envFrom` values let you mount a Secret or ConfigMap with credentials or provider config.
