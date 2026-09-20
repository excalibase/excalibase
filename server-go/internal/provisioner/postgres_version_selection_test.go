package provisioner

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-408: the Docker deployment path honours the requested major instead of
// ignoring it, and refuses anything the catalogue does not list.

func TestDockerProvisionUsesTheRequestedMajor(t *testing.T) {
	for _, major := range config.PostgresMajors() {
		docker := newMockDocker()
		p := NewDockerPostgreSQLProvisioner(docker)
		_, err := p.Provision(context.Background(), domain.ProvisioningRequest{
			ProjectName:     "blog-" + major,
			OrgID:           "org1",
			DBType:          domain.PostgreSQL,
			Tier:            domain.Free,
			PostgresVersion: major,
		}, config.TierConfig{}, func(domain.ProvisioningStage) {})
		if err != nil {
			t.Fatalf("major %s: provision: %v", major, err)
		}
		if want := "postgres:" + major; docker.lastImage != want {
			t.Errorf("major %s: container image %q, want %q", major, docker.lastImage, want)
		}
	}
}

func TestDockerProvisionRefusesAMissingOrUnsupportedMajor(t *testing.T) {
	for _, major := range []string{"", "  ", "13", "19", "latest"} {
		docker := newMockDocker()
		p := NewDockerPostgreSQLProvisioner(docker)
		_, err := p.Provision(context.Background(), domain.ProvisioningRequest{
			ProjectName:     "blog",
			OrgID:           "org1",
			DBType:          domain.PostgreSQL,
			Tier:            domain.Free,
			PostgresVersion: major,
		}, config.TierConfig{}, func(domain.ProvisioningStage) {})
		if err == nil {
			t.Errorf("major %q: expected a refusal", major)
		}
		if docker.lastImage != "" {
			t.Errorf("major %q: a container was created anyway (image %q)", major, docker.lastImage)
		}
	}
}
