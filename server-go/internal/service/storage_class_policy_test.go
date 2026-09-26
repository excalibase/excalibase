package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func provisionWithClass(t *testing.T, svc *ProvisioningService, class string) (*domain.ProvisioningResponse, error) {
	t.Helper()
	return svc.Provision(context.Background(), domain.ProvisioningRequest{
		PostgresVersion: "17", ProjectName: "p", OrgID: "org", DBType: domain.PostgreSQL, StorageClass: class,
	})
}

func clusterStorageClass(t *testing.T, mock *k8s.MockClient, resp *domain.ProvisioningResponse) (string, bool) {
	t.Helper()
	cluster := mock.CRDs[resp.Namespace+"/"+resp.ProjectID+"-postgres"]
	if cluster == nil {
		t.Fatalf("no cluster applied for %s: %v", resp.ProjectID, mock.CRDs)
	}
	class, found, _ := unstructured.NestedString(cluster.Object, "spec", "storage", "storageClass")
	return class, found
}

func TestProvisionRunsOnThePlatformDefaultStorageClass(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	svc.SetStorageClassPolicy(config.StorageClassPolicy{Default: "standard-rwo", Allowed: []string{"fast-ssd"}})

	resp, err := provisionWithClass(t, svc, "")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if class, _ := clusterStorageClass(t, mock, resp); class != "standard-rwo" {
		t.Errorf("storage class = %q, want the platform default", class)
	}
}

func TestProvisionAcceptsAnAllowedStorageClass(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	svc.SetStorageClassPolicy(config.StorageClassPolicy{Default: "standard-rwo", Allowed: []string{"fast-ssd"}})

	resp, err := provisionWithClass(t, svc, "fast-ssd")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if class, _ := clusterStorageClass(t, mock, resp); class != "fast-ssd" {
		t.Errorf("storage class = %q, want the requested allowed class", class)
	}
}

func TestProvisionRefusesAStorageClassThePlatformDoesNotOffer(t *testing.T) {
	cases := map[string]config.StorageClassPolicy{
		"not on the allowlist": {Default: "standard-rwo", Allowed: []string{"fast-ssd"}},
		"no policy configured": {},
	}
	for name, policy := range cases {
		t.Run(name, func(t *testing.T) {
			svc, store, mock := setupProvisioningTest(t)
			svc.SetStorageClassPolicy(policy)

			if _, err := provisionWithClass(t, svc, "local-path"); !errors.Is(err, config.ErrStorageClassNotAllowed) {
				t.Fatalf("Provision = %v, want ErrStorageClassNotAllowed", err)
			}
			if all, _ := store.FindAll(); len(all) != 0 {
				t.Errorf("no project may be recorded: %v", all)
			}
			if len(mock.CRDs) != 0 || len(mock.Namespaces) != 0 {
				t.Errorf("nothing may be created: crds=%v ns=%v", mock.CRDs, mock.Namespaces)
			}
		})
	}
}

func TestProvisionWithoutAPolicyUsesTheClusterDefault(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)

	resp, err := provisionWithClass(t, svc, "")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if class, found := clusterStorageClass(t, mock, resp); found {
		t.Errorf("no configured default must leave the class to the cluster, got %q", class)
	}
}
