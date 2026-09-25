#!/bin/bash
set -e

echo "🚀 Installing Kubernetes Database Operators"
echo "=========================================="
echo ""

# Check if kubectl is available
if ! command -v kubectl &> /dev/null; then
    echo "❌ kubectl not found. Please install kubectl first."
    exit 1
fi

# Check if cluster is running
if ! kubectl cluster-info &> /dev/null; then
    echo "❌ Kubernetes cluster not accessible. Please start your cluster."
    exit 1
fi

echo "✅ Kubernetes cluster is accessible"
echo ""

# Install CloudNativePG (PostgreSQL)
echo "📦 Installing CloudNativePG operator (PostgreSQL)..."
kubectl apply -f https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.30/releases/cnpg-1.30.1.yaml

echo ""
echo "⏳ Waiting for CloudNativePG operator to be ready..."
kubectl wait --for=condition=available --timeout=120s deployment/cnpg-controller-manager -n cnpg-system

echo ""
echo "✅ CloudNativePG operator installed successfully!"
echo ""

# Verify installation
echo "🔍 Verifying installation..."
kubectl get pods -n cnpg-system
echo ""

echo "✅ All operators installed and ready!"
echo ""
echo "Next steps:"
echo "  1. Start the provisioning service: java -jar target/provisioning-poc-0.0.1-SNAPSHOT.jar"
echo "  2. Provision a database: curl -X POST http://localhost:8080/api/provision ..."
echo ""
