# Matrix 1: K8s in-cluster

The platform runs inside the same Kubernetes cluster it provisions databases
on. `client-go` picks up the mounted ServiceAccount automatically. This is the
recommended production topology.

Works on any K8s distro — managed (EKS/GKE/AKS), self-hosted multi-node
(RKE2), or single-node (k0s, k3s, MicroK8s — including rootless). Single-node
distros make this matrix viable for homelab / VPS deployments while keeping
the ServiceAccount + Role + namespace isolation surface that Docker mode
lacks.

## Prerequisites

- Kubernetes 1.27+
- [CloudNativePG](https://cloudnative-pg.io/) operator installed
- [metrics-server](https://github.com/kubernetes-sigs/metrics-server) installed
- An ingress controller (nginx, traefik, etc.)
- cert-manager (recommended for TLS)

## Install operators

```bash
kubectl apply --server-side -f \
  https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.30/releases/cnpg-1.30.1.yaml

kubectl apply -f \
  https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
```

## ServiceAccount and RBAC

The platform needs cluster-wide permissions to manage namespaces, CRDs,
Secrets, Pods, and Helm releases.

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: excalibase-provisioning
  namespace: excalibase-platform
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: excalibase-provisioning
rules:
  - apiGroups: [""]
    resources: ["namespaces", "secrets", "pods", "pods/exec", "pods/log", "services", "configmaps"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["apps"]
    resources: ["deployments", "statefulsets"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["postgresql.cnpg.io"]
    resources: ["clusters", "scheduledbackups", "backups", "poolers"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
  - apiGroups: ["metrics.k8s.io"]
    resources: ["pods"]
    verbs: ["get", "list"]
  - apiGroups: ["networking.k8s.io"]
    resources: ["ingresses", "networkpolicies"]
    verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: excalibase-provisioning
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: excalibase-provisioning
subjects:
  - kind: ServiceAccount
    name: excalibase-provisioning
    namespace: excalibase-platform
```

## Helm install

The repo ships a Helm chart at `charts/provisioning/`. Minimal values for
self-hosted:

```yaml
# values.yaml
namespace: excalibase-platform
image:
  repository: excalibase/provisioning
  tag: "0.1.0"

env:
  DEPLOYMENT_MODE: "selfhosted"
  CORS_ORIGINS: "https://your-studio.example.com"
  PUBLIC_BASE_URL: "https://api.your-domain.example.com"
  DENO_RUNTIME_SECRET: "" # generate with: openssl rand -hex 32

ingress:
  enabled: true
  host: api.your-domain.example.com
```

For cloud mode, add:

```yaml
env:
  DEPLOYMENT_MODE: "cloud"

platformDB:
  enabled: true
  existingSecret: platform-db-credentials
  existingSecretKey: PLATFORM_DB_URL
```

Then:

```bash
helm install provisioning ./charts/provisioning -n excalibase-platform -f values.yaml
```

## How client-go finds the cluster

When the platform pod is running inside Kubernetes with a ServiceAccount
mounted, `client-go` reads `/var/run/secrets/kubernetes.io/serviceaccount/`
and connects to `kubernetes.default.svc`. You do **not** need to set
`KUBE_API_URL`, `KUBECONFIG_PATH`, or any of the remote-cluster variables.

Fallback order (from `internal/k8s/client.go`):

1. `KUBE_API_URL` + `KUBE_BEARER_TOKEN` (remote API — matrix 2)
2. `KUBECONFIG_PATH` (explicit file path)
3. `KUBECONFIG` env var
4. **In-cluster config** — this is what matrix 1 uses
5. `~/.kube/config` fallback

## Verification

```bash
# Pod should be Running
kubectl get pods -n excalibase-platform

# Health check
kubectl port-forward -n excalibase-platform svc/provisioning 24005:24005 &
curl http://localhost:24005/healthz

# Create a project (self-hosted uses the default org)
curl -X POST http://localhost:24005/api/provision \
  -H "Content-Type: application/json" \
  -d '{"projectName":"test","orgId":"default","databaseType":"POSTGRESQL","tier":"FREE"}'
```

## Troubleshooting

- **`forbidden: cannot list resource ...`** — ClusterRoleBinding not applied
  or ServiceAccount name mismatch. Re-check the YAML above.
- **`dial tcp: lookup kubernetes.default.svc: no such host`** — pod isn't
  running in-cluster, or the ServiceAccount token isn't mounted. Check
  `automountServiceAccountToken`.
- **Edge functions fail with 502** — `DENO_RUNTIME_SECRET` not set or
  mismatched between platform and Deno runtime pods.
