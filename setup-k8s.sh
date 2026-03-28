#!/bin/bash
set -e

echo "🚀 Excalibase Provisioning POC - Kubernetes Setup"
echo "=================================================="
echo ""

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Check prerequisites
echo "📋 Checking prerequisites..."

# Check Docker
if ! command -v docker &> /dev/null; then
    echo -e "${RED}❌ Docker is not installed${NC}"
    exit 1
fi
echo -e "${GREEN}✓ Docker is installed${NC}"

# Check if we have minikube or kind
HAS_MINIKUBE=false
HAS_KIND=false

if command -v minikube &> /dev/null; then
    HAS_MINIKUBE=true
    echo -e "${GREEN}✓ Minikube is installed${NC}"
fi

if command -v kind &> /dev/null; then
    HAS_KIND=true
    echo -e "${GREEN}✓ Kind is installed${NC}"
fi

if [ "$HAS_MINIKUBE" = false ] && [ "$HAS_KIND" = false ]; then
    echo -e "${YELLOW}⚠️  Neither minikube nor kind is installed${NC}"
    echo ""
    echo "Please install one of them:"
    echo "  Minikube: https://minikube.sigs.k8s.io/docs/start/"
    echo "  Kind: https://kind.sigs.k8s.io/docs/user/quick-start/"
    exit 1
fi

# Check kubectl
if ! command -v kubectl &> /dev/null; then
    echo -e "${RED}❌ kubectl is not installed${NC}"
    echo "Install: https://kubernetes.io/docs/tasks/tools/"
    exit 1
fi
echo -e "${GREEN}✓ kubectl is installed${NC}"
echo ""

# Choose cluster tool
CLUSTER_TOOL=""
if [ "$HAS_KIND" = true ]; then
    CLUSTER_TOOL="kind"
    echo "Using Kind for Kubernetes cluster"
elif [ "$HAS_MINIKUBE" = true ]; then
    CLUSTER_TOOL="minikube"
    echo "Using Minikube for Kubernetes cluster"
fi

# Start Kubernetes cluster
echo ""
echo "🏗️  Starting Kubernetes cluster..."

if [ "$CLUSTER_TOOL" = "kind" ]; then
    # Check if cluster already exists
    if kind get clusters | grep -q "excalibase"; then
        echo -e "${YELLOW}Cluster 'excalibase' already exists, deleting...${NC}"
        kind delete cluster --name excalibase
    fi

    kind create cluster --name excalibase --wait 60s
    kubectl cluster-info --context kind-excalibase

elif [ "$CLUSTER_TOOL" = "minikube" ]; then
    # Check if minikube is running
    if minikube status &> /dev/null; then
        echo -e "${YELLOW}Minikube is already running${NC}"
    else
        minikube start --cpus=4 --memory=8192
    fi
    kubectl config use-context minikube
fi

echo -e "${GREEN}✓ Kubernetes cluster is ready${NC}"
echo ""

# Install CloudNativePG (PostgreSQL operator)
echo "🐘 Installing CloudNativePG operator (PostgreSQL)..."
kubectl apply -f https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.23/releases/cnpg-1.23.0.yaml

echo "Waiting for CloudNativePG operator to be ready..."
kubectl wait --for=condition=available --timeout=120s deployment/cnpg-controller-manager -n cnpg-system 2>/dev/null || true

echo -e "${GREEN}✓ CloudNativePG operator installed${NC}"
echo ""

# Note: Vitess and MongoDB operators are more complex to install
# For POC, we'll focus on PostgreSQL first
echo -e "${YELLOW}ℹ️  Note: Only CloudNativePG (PostgreSQL) is installed for initial testing${NC}"
echo -e "${YELLOW}   Vitess and MongoDB operators can be added later${NC}"
echo ""

# Verify cluster
echo "🔍 Verifying cluster setup..."
kubectl get nodes
echo ""
kubectl get pods -n cnpg-system
echo ""

echo -e "${GREEN}✅ Kubernetes setup complete!${NC}"
echo ""
echo "Next steps:"
echo "  1. Build the service: ./mvnw clean package"
echo "  2. Run the service: java -jar target/provisioning-poc-0.0.1-SNAPSHOT.jar"
echo "  3. Test provisioning: ./test-provision.sh"
echo ""
