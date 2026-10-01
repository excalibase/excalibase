package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type endpointCapableStore struct {
	storage.PlatformStore
	storage.DatabaseEndpointStore
}

// EXC-528: without a public endpoint domain, Studio still needs the
// in-cluster address and the CA, so the service is wired on Kubernetes and
// answers for the in-cluster half only.
func TestTheDBEndpointServiceIsWiredOnKubernetesWithoutAPublicDomain(t *testing.T) {
	cfg := config.AppConfig{ProvisionerMode: "k8s"}
	if buildDBEndpointService(cfg, endpointCapableStore{}, nil, k8s.NewMockClient(), nil) == nil {
		t.Fatal("no endpoint service on Kubernetes without a public domain")
	}
}

func TestTheDBEndpointServiceIsNotWiredWithoutKubernetes(t *testing.T) {
	cfg := config.AppConfig{ProvisionerMode: "docker", DBEndpointDomain: "db.example.com"}
	if buildDBEndpointService(cfg, endpointCapableStore{}, nil, k8s.NewMockClient(), nil) != nil {
		t.Fatal("docker mode has no Service to describe or publish")
	}
}
