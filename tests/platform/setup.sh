#!/bin/bash
set -euo pipefail

NS="excalibase-platform"
CNPG_VERSION="1.30.1"

echo "=== 1. Create namespace ==="
kubectl create namespace $NS --dry-run=client -o yaml | kubectl apply -f -

echo "=== 2. Deploy NATS (JetStream) ==="
kubectl apply -n $NS -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: nats
  labels:
    app: nats
spec:
  containers:
    - name: nats
      image: nats:2.10-alpine
      args: ["-js", "-m", "8222", "--store_dir", "/data"]
      ports:
        - containerPort: 4222
        - containerPort: 8222
      resources:
        requests: { cpu: 50m, memory: 64Mi }
---
apiVersion: v1
kind: Service
metadata:
  name: nats
spec:
  ports:
    - name: client
      port: 4222
    - name: monitoring
      port: 8222
  selector:
    app: nats
EOF
kubectl wait --for=condition=Ready pod/nats -n $NS --timeout=60s

echo "=== 3. Deploy platform-db (CNPG) ==="
kubectl apply -n $NS -f - <<'EOF'
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: platform-db
spec:
  instances: 2
  imageName: ghcr.io/cloudnative-pg/postgresql:16.4
  resources:
    requests: { cpu: 100m, memory: 256Mi }
    limits: { cpu: 500m, memory: 512Mi }
  storage:
    size: 2Gi
  bootstrap:
    initdb:
      database: platform
      owner: platform
  postgresql:
    parameters:
      shared_buffers: "64MB"
      max_connections: "100"
EOF
kubectl wait --for=condition=Ready cluster/platform-db -n $NS --timeout=300s
echo "  platform-db ready"

echo "=== 4. Get platform-db credentials ==="
PLATFORM_PASS=$(kubectl get secret platform-db-app -n $NS -o jsonpath='{.data.password}' | base64 -d)
PLATFORM_DB_URL="postgres://platform:${PLATFORM_PASS}@platform-db-rw.${NS}.svc.cluster.local:5432/platform?sslmode=disable"
echo "  URL: ${PLATFORM_DB_URL:0:50}..."

echo "=== 5. Deploy Provisioning ==="
kubectl apply -n $NS -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: provisioning-sa
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: provisioning-admin
subjects:
  - kind: ServiceAccount
    name: provisioning-sa
    namespace: $NS
roleRef:
  kind: ClusterRole
  name: cluster-admin
  apiGroup: rbac.authorization.k8s.io
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: provisioning
spec:
  replicas: 1
  selector:
    matchLabels:
      app: provisioning
  template:
    metadata:
      labels:
        app: provisioning
    spec:
      serviceAccountName: provisioning-sa
      containers:
        - name: provisioning
          image: excalibase/provisioning:latest
          imagePullPolicy: Never
          env:
            - name: PORT
              value: "24005"
            - name: STORAGE_PATH
              value: /data
            - name: PLATFORM_DB_URL
              value: "$PLATFORM_DB_URL"
            - name: NATS_URL
              value: "nats://nats.${NS}.svc.cluster.local:4222"
            - name: CORS_ORIGINS
              value: "*"
          ports:
            - containerPort: 24005
          volumeMounts:
            - name: data
              mountPath: /data
      volumes:
        - name: data
          emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: provisioning
spec:
  ports:
    - port: 24005
  selector:
    app: provisioning
EOF
kubectl wait --for=condition=Ready pod -l app=provisioning -n $NS --timeout=120s
echo "  Provisioning ready"

echo "=== 6. Deploy Auth ==="
kubectl apply -n $NS -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: auth
spec:
  replicas: 1
  selector:
    matchLabels:
      app: auth
  template:
    metadata:
      labels:
        app: auth
    spec:
      containers:
        - name: auth
          image: excalibase/auth:sh2
          imagePullPolicy: Never
          env:
            - name: PORT
              value: "24000"
            - name: PROVISIONING_URL
              value: "http://provisioning:24005/api"
            - name: PROVISIONING_PAT
              value: "PLACEHOLDER"
            - name: CORS_ORIGINS
              value: "*"
          ports:
            - containerPort: 24000
---
apiVersion: v1
kind: Service
metadata:
  name: auth
spec:
  ports:
    - port: 24000
  selector:
    app: auth
EOF
echo "  Auth deployed (will start once vault is initialized and PAT is created)"

echo ""
echo "=== Platform deployed ==="
echo "Next steps:"
echo "  1. Port-forward: kubectl port-forward svc/provisioning 24005:24005 -n $NS"
echo "  2. Init vault: curl -X POST http://localhost:24005/api/vault/init -d '{\"shares\":1,\"threshold\":1}'"
echo "  3. Login: curl -X POST http://localhost:24005/api/auth/login -d '{\"username\":\"admin\",\"password\":\"<from bootstrap>\"}'"
echo "  4. Create PAT for auth service"
echo "  5. Update auth deployment with real PAT"
echo "  6. Create org + provision project"
