# Driftd Helm Chart

This chart deploys the Driftd server and worker components.

## Prerequisites

- Helm v3
- A storage class that supports your desired access mode (RWX recommended for shared data/cache)

## Install

```bash
helm install driftd ./helm/driftd \
  --set image.repository=ghcr.io/driftdhq/driftd \
  --set image.tag=v0.1.3
```

## Values

Key values in `values.yaml`:

- `image.repository`, `image.tag`, `image.pullPolicy`
- `image.digest` (takes precedence over tag for immutable deploys)
- `image.pullSecrets` for private registries
- `service.type`, `service.port`
- `networkPolicy.*` (optional, user-defined policies; disabled by default)
- `podDisruptionBudget.*` (optional, user-defined budgets; disabled by default)
- `serviceAccount.*` (including IRSA/workload identity annotations)
- `server.replicas`, `server.resources`, `server.envFrom`, `server.readinessProbe`, `server.livenessProbe`
- `worker.replicas`, `worker.resources`, `worker.envFrom`, `worker.livenessProbe`
- `config.worker.clone_depth` (git clone depth for standalone scans; default `1`)
- `storage.data` and `storage.cache` PVC settings
- `config`: the Driftd `config.yaml` rendered into a ConfigMap

If `image.tag` is empty, the chart uses `Chart.appVersion`.
If `image.digest` is set, the chart uses `<repository>@<digest>` and ignores `image.tag`.

## Optional NetworkPolicy / PDB

This chart does not create `NetworkPolicy` or `PodDisruptionBudget` resources by default.

If you want them, enable and define them via values:

```yaml
networkPolicy:
  enabled: true
  policies:
    - name: worker-egress
      podSelector:
        matchLabels:
          app.kubernetes.io/name: driftd
          app.kubernetes.io/instance: driftd
          app.kubernetes.io/component: worker
      policyTypes: ["Egress"]
      egress:
        - to:
            - namespaceSelector: {}
          ports:
            - protocol: TCP
              port: 6379

podDisruptionBudget:
  enabled: true
  budgets:
    - name: server
      minAvailable: 1
      selector:
        matchLabels:
          app.kubernetes.io/name: driftd
          app.kubernetes.io/instance: driftd
          app.kubernetes.io/component: server
```

IRSA / workload identity example:

```yaml
serviceAccount:
  create: true
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::<account-id>:role/driftd-readonly
```

## Auth Modes

driftd supports two auth modes via `config.auth.mode`:

- `internal` (default): use `config.ui_auth` / `config.api_auth` credentials.
- `external`: trust identity/group headers from an upstream auth proxy (for example oauth2-proxy).

When using `external`, configure role mapping under `config.auth.external.roles`:

- `viewers`: read-only access
- `operators`: can trigger scans
- `admins`: full settings/API admin access

Example files for oauth2-proxy are provided in:

- `helm/driftd/examples/oauth2-proxy/`

Production baseline values example:

- `helm/driftd/examples/values-prod-example.yaml`

## Required In Secure Mode

When `config.insecure_dev_mode=false` (recommended), driftd requires:

- authentication configured (`config.auth.mode=internal` with credentials, or `config.auth.mode=external` with trusted proxy headers)
- `DRIFTD_ENCRYPTION_KEY` available in both server and worker pods

Example secret wiring:

```yaml
server:
  envFrom:
    - secretRef:
        name: driftd-runtime
worker:
  envFrom:
    - secretRef:
        name: driftd-runtime
```

## Example Values

```yaml
config:
  data_dir: /data
  redis:
    addr: "redis:6379"
  worker:
    block_external_data_source: true
  projects:
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

This chart installs Bitnami Redis by default (`redis.enabled=true`).

Default Redis settings in `values.yaml` are development-oriented:

- `redis.auth.enabled=false`
- `redis.master.persistence.enabled=false`
- `redis.networkPolicy.enabled=false`
- `redis.master.pdb.create=false`
- `redis.replica.pdb.create=false`

For production, enable Redis auth and persistence or use external managed Redis.

To use external Redis instead:

- set `redis.enabled=false`
- set `config.redis.addr` (and `config.redis.password` if needed)

## Storage

Driftd uses two PVCs by default:

- `storage.data`: Plan outputs, status files, workspace snapshots
- `storage.cache`: Terraform provider cache and tfswitch/tgswitch binaries

For multi-worker deployments, use `ReadWriteMany` volumes if possible. If only `ReadWriteOnce` is available, keep workers on a single node or reduce worker replicas to 1.

## Credentials

### Git credentials

- **SSH**: Mount keys at the paths referenced in `config.projects[].git`.
- **HTTPS token**: Use an environment variable and reference it via `https_token_env`.
- **GitHub App**: Mount the private key and set `app_id`, `installation_id`, and `private_key_path`.

### Cloud provider credentials (Terraform)

Terraform runs in the worker container and uses whatever credentials you provide. Options include:

- Environment variables (AWS/GCP/Azure standard vars)
- Mounted credential files (AWS shared config, GCP JSON key, Azure profile)
- Kubernetes workload identity (EKS IRSA, GKE Workload Identity, Azure Workload Identity)

The `server.envFrom` and `worker.envFrom` values let you mount a Secret or ConfigMap with credentials or provider config.
