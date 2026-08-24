# Matrix 2: K8s remote

The platform runs outside Kubernetes (binary on a VM, Docker container, or a
different cluster) and provisions databases on a **remote** Kubernetes cluster
via its API server URL and a ServiceAccount bearer token.

Use this when:

- You want the platform to be a long-running binary on a VM, not a pod.
- You run the platform in a management cluster and provision to tenant
  clusters.
- You're developing locally against a remote minikube / kind / EKS.

## Target cluster setup

On the **target cluster**, create a ServiceAccount + ClusterRoleBinding
(same RBAC as matrix 1 — see [k8s-in-cluster.md](./k8s-in-cluster.md)), then
generate a long-lived token.

```yaml
# sa-token-secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: excalibase-provisioning-token
  namespace: excalibase-platform
  annotations:
    kubernetes.io/service-account.name: excalibase-provisioning
type: kubernetes.io/service-account-token
```

```bash
kubectl apply -f sa-token-secret.yaml

# Extract the token and CA cert
TOKEN=$(kubectl get secret excalibase-provisioning-token \
  -n excalibase-platform -o jsonpath='{.data.token}' | base64 -d)

CA_CERT=$(kubectl get secret excalibase-provisioning-token \
  -n excalibase-platform -o jsonpath='{.data.ca\.crt}' | base64 -d)

API_URL=$(kubectl cluster-info | grep 'control plane' | \
  awk -F'at ' '{print $2}' | sed 's/\x1b\[[0-9;]*m//g')
```

## Platform environment

Run the platform binary (or a Docker container) with these environment
variables:

```bash
export DEPLOYMENT_MODE=selfhosted
export CORS_ORIGINS="https://studio.example.com"
export PUBLIC_BASE_URL="https://api.example.com"
export STORAGE_PATH=/var/lib/excalibase
# Platform store — Postgres required in both modes (no SQLite). Point at a
# Postgres reachable from this host (local container or managed PG).
export PLATFORM_DB_URL="postgres://platform:CHANGEME@platform-db:5432/platform?sslmode=disable"
export DENO_RUNTIME_SECRET=$(openssl rand -hex 32)

# Remote K8s API
export KUBE_API_URL="$API_URL"
export KUBE_BEARER_TOKEN="$TOKEN"

# Option A: pass CA cert inline
export KUBE_CA_CERT="$CA_CERT"

# Option B: disable TLS verification (DEV ONLY — do not use in production)
# export KUBE_INSECURE_SKIP_VERIFY=true

./excalibase-server
```

## How client-go resolves the target

From `internal/k8s/client.go`, the priority order is:

1. **`KUBE_API_URL` + `KUBE_BEARER_TOKEN`** ← this matrix
2. `KUBECONFIG_PATH`
3. `KUBECONFIG` env var
4. In-cluster config
5. `~/.kube/config`

If both `KUBE_API_URL` and `KUBE_BEARER_TOKEN` are set, the client builds a
`rest.Config` directly — no kubeconfig file needed.

- TLS: if `KUBE_CA_CERT` is set, it's used as the CA bundle. Otherwise the
  system trust store is used. `KUBE_INSECURE_SKIP_VERIFY=true` disables
  verification entirely.
- The target cluster must be network-reachable from wherever the platform
  runs. For clouds, this usually means a public API endpoint or a VPN/
  PrivateLink to a private one.

## Running as a systemd service

```ini
# /etc/systemd/system/excalibase-provisioning.service
[Unit]
Description=Excalibase Provisioning
After=network.target

[Service]
Type=simple
User=excalibase
EnvironmentFile=/etc/excalibase/provisioning.env
ExecStart=/usr/local/bin/excalibase-server
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

`/etc/excalibase/provisioning.env`:

```env
DEPLOYMENT_MODE=selfhosted
CORS_ORIGINS=https://studio.example.com
PUBLIC_BASE_URL=https://api.example.com
STORAGE_PATH=/var/lib/excalibase
# Postgres required in both modes (no SQLite fallback).
PLATFORM_DB_URL=postgres://platform:CHANGEME@platform-db:5432/platform?sslmode=disable
DENO_RUNTIME_SECRET=<32-byte hex>
KUBE_API_URL=https://target-cluster.example.com:6443
KUBE_BEARER_TOKEN=<token>
KUBE_CA_CERT=<pem with \n escaped>
```

## Verification

```bash
./excalibase-server &
curl http://localhost:24005/healthz

# Should successfully create a project on the remote cluster
curl -X POST http://localhost:24005/api/provision \
  -H "Content-Type: application/json" \
  -d '{"projectName":"remote-test","orgId":"default","databaseType":"POSTGRESQL","tier":"FREE"}'

# On the target cluster, verify the namespace was created
kubectl --context=target get ns | grep default-remote-test
```

## Troubleshooting

- **`Unauthorized` or `401`** — token expired or wrong ServiceAccount. The
  Secret annotation must match the ServiceAccount name.
- **`x509: certificate signed by unknown authority`** — `KUBE_CA_CERT` missing
  or malformed. Escape newlines as `\n` when setting via env file, or point to
  the file contents correctly.
- **Timeouts** — platform host can't reach the cluster API. Check firewall
  rules, security groups, or VPN.
- **`forbidden`** — RBAC on target cluster insufficient. Re-apply the
  ClusterRole from matrix 1 on the target.
