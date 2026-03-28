# Deno Edge Functions Runtime

Self-hosted serverless platform. Deploy TypeScript functions that run in Deno Web Workers on Kubernetes.

## Build

```bash
docker build -t excalibase/deno-runtime:latest .
```

## Deploy to Kubernetes

```bash
# Load image into minikube (no registry needed)
minikube image load excalibase/deno-runtime:latest

# Deploy
kubectl apply -f k8s/

# Verify
kubectl get pods -n serverless
```

## Test

```bash
# Port-forward
kubectl port-forward -n serverless svc/deno-runtime 24006:8000 &

# Health
curl http://localhost:24006/health

# Deploy a function
curl -X POST http://localhost:24006/deploy \
  -H "Content-Type: application/json" \
  -d '{"id":"hello","code":"function handler(name) { return \"Hello, \" + name + \"!\"; }"}'

# Invoke
curl -X POST http://localhost:24006/invoke/hello \
  -H "Content-Type: application/json" \
  -d '"World"'
# → "Hello, World!"
```

## API

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | /health | Health check |
| POST | /deploy | Deploy function `{id, code}` |
| POST | /invoke/{id} | Invoke function |
| DELETE | /delete/{id} | Delete function |
| GET | /scripts | List all functions |
| GET | /stats | Server statistics |

## Architecture

Each function runs in an isolated Deno Web Worker with `net` permission only. 10-50ms cold start, 1-5ms warm invocation, up to 500 functions per pod.
