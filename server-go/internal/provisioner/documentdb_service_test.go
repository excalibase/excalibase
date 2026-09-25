package provisioner

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

func documentDBRequest(documentDB bool) domain.ProvisioningRequest {
	majors := config.DocumentDBMajors()
	return domain.ProvisioningRequest{
		ProjectName:     "docproj",
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
		Tier:            domain.Free,
		PostgresVersion: majors[len(majors)-1],
		DocumentDB:      documentDB,
	}
}

var documentDBTier = config.TierConfig{Instances: 1, StorageSize: "5Gi"}

func provisionBothWays(t *testing.T, documentDB bool) []*k8s.MockClient {
	t.Helper()
	plain := documentDBKube()
	if _, err := NewPostgreSQLProvisioner(plain, "").Provision(context.Background(), documentDBRequest(documentDB), documentDBTier,
		func(domain.ProvisioningStage) {}); err != nil {
		t.Fatalf("provision: %v", err)
	}
	rollback := documentDBKube()
	if _, err := NewPostgreSQLProvisioner(rollback, "").ProvisionWithRollback(context.Background(), documentDBRequest(documentDB), documentDBTier,
		NewProvisionContext(nil, nil)); err != nil {
		t.Fatalf("provision with rollback: %v", err)
	}
	return []*k8s.MockClient{plain, rollback}
}

func TestADocumentDBProjectGetsAnInClusterGatewayService(t *testing.T) {
	for _, mock := range provisionBothWays(t, true) {
		if !mock.DocumentDBServices["org1-docproj/docproj"] {
			t.Errorf("no gateway service was created: %v", mock.Calls)
		}
	}
}

func TestAPlainProjectGetsNoGatewayService(t *testing.T) {
	for _, mock := range provisionBothWays(t, false) {
		if len(mock.DocumentDBServices) != 0 {
			t.Errorf("a plain project got a gateway service: %v", mock.DocumentDBServices)
		}
	}
}

func TestAGatewayServiceFailureFailsTheProvision(t *testing.T) {
	refused := errors.New("refused")
	plain := documentDBKube()
	plain.EnsureDocumentDBServiceError = refused
	if _, err := NewPostgreSQLProvisioner(plain, "").Provision(context.Background(), documentDBRequest(true), documentDBTier,
		func(domain.ProvisioningStage) {}); !errors.Is(err, refused) {
		t.Errorf("provision: got %v, want the refusal", err)
	}
	rollback := documentDBKube()
	rollback.EnsureDocumentDBServiceError = refused
	if _, err := NewPostgreSQLProvisioner(rollback, "").ProvisionWithRollback(context.Background(), documentDBRequest(true), documentDBTier,
		NewProvisionContext(nil, nil)); !errors.Is(err, refused) {
		t.Errorf("provision with rollback: got %v, want the refusal", err)
	}
}
