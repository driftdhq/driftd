# driftd + oauth2-proxy (OIDC) Example

This example shows how to run driftd behind oauth2-proxy using external auth mode.
It is designed for ingress-nginx and works with Okta, Entra ID, Google, or any OIDC provider.

## What this gives you

- Browser SSO via OIDC (oauth2-proxy session cookie)
- Group-to-role mapping in driftd (`viewer`, `operator`, `admin`)
- No driftd basic-auth prompts for users

## Files

- `driftd-values-external-auth.yaml`: driftd auth mode and role mapping example.
- `oauth2-proxy-values.yaml`: oauth2-proxy Helm values template.
- `ingress-nginx-auth.yaml`: ingress with auth annotations and header forwarding.
- `ingress-nginx-webhook.yaml`: optional unauthenticated webhook ingress path.

## 1) Prepare runtime secrets

Create driftd runtime secret (encryption key):

```bash
kubectl create secret generic driftd-runtime \
  --from-literal=DRIFTD_ENCRYPTION_KEY="$(openssl rand -base64 32)" \
  --dry-run=client -o yaml | kubectl apply -f -
```

Create oauth2-proxy secret values (replace placeholders):

```bash
kubectl create secret generic oauth2-proxy \
  --from-literal=client-id='REPLACE_ME' \
  --from-literal=client-secret='REPLACE_ME' \
  --from-literal=cookie-secret="$(openssl rand -base64 32 | tr -d '\n' | cut -c1-32)" \
  --dry-run=client -o yaml | kubectl apply -f -
```

## 2) Install driftd in external auth mode

```bash
helm upgrade --install driftd ./helm/driftd \
  -f ./helm/driftd/examples/oauth2-proxy/driftd-values-external-auth.yaml
```

## 3) Install oauth2-proxy

```bash
helm repo add oauth2-proxy https://oauth2-proxy.github.io/manifests
helm repo update

helm upgrade --install oauth2-proxy oauth2-proxy/oauth2-proxy \
  -f ./helm/driftd/examples/oauth2-proxy/oauth2-proxy-values.yaml
```

## 4) Install ingress resources

```bash
kubectl apply -f ./helm/driftd/examples/oauth2-proxy/ingress-nginx-auth.yaml
```

If you use GitHub webhooks, also apply:

```bash
kubectl apply -f ./helm/driftd/examples/oauth2-proxy/ingress-nginx-webhook.yaml
```

## Okta-specific notes

- Include `groups` in oauth2-proxy scopes: `openid profile email groups`.
- Ensure the groups claim is present in ID token/userinfo.
- In driftd `auth.external.roles`, map Okta group names to roles.

## Security notes

- Keep driftd `Service` as `ClusterIP` and only expose through ingress.
- In external mode, driftd trusts proxy headers; do not expose driftd directly.
- Protect `/api/webhooks/github` with `webhook.github_secret` even when bypassing OIDC.
