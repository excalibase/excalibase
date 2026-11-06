package provisioner

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type imageRecordingDocker struct {
	DockerClient
	images []string
}

func (d *imageRecordingDocker) EnsureImage(_ context.Context, image string) error {
	d.images = append(d.images, image)
	return nil
}

func TestDockerPostgres_PreparesTheBuildsImages(t *testing.T) {
	for name, documentDB := range map[string]bool{"postgres": false, "documentdb": true} {
		t.Run(name, func(t *testing.T) {
			docker := &imageRecordingDocker{}
			prov := &DockerPostgreSQLProvisioner{docker: docker}
			req := domain.ProvisioningRequest{PostgresVersion: "17", DocumentDB: documentDB}
			if documentDB {
				req.PostgresVersion = "17"
			}
			if err := prov.PrepareImages(context.Background(), req); err != nil {
				t.Fatalf("PrepareImages: %v", err)
			}
			if len(docker.images) == 0 {
				t.Fatal("no image fetched ahead of the build")
			}
		})
	}
}

// A client with no way to fetch is left to fetch at container creation.
func TestDockerPostgres_PreparingWithoutAFetchIsANoOp(t *testing.T) {
	prov := &DockerPostgreSQLProvisioner{docker: nil}
	if err := prov.PrepareImages(context.Background(), domain.ProvisioningRequest{PostgresVersion: "17"}); err != nil {
		t.Fatalf("PrepareImages: %v", err)
	}
}

func TestDockerMySQL_PreparesItsImage(t *testing.T) {
	engine := newFakeEngine()
	docker := &imageRecordingDocker{DockerClient: engine}
	prov := NewDockerMySQLProvisioner(docker)
	if err := prov.PrepareImages(context.Background(), dockerMySQLRequest()); err != nil {
		t.Fatalf("PrepareImages: %v", err)
	}
	if len(docker.images) != 1 {
		t.Fatalf("images %v, want the catalogue's one", docker.images)
	}
}

// A request naming nothing listed is for Provision to refuse, as it always has.
func TestDockerMySQL_PreparingARequestItWillRefuseIsANoOp(t *testing.T) {
	docker := &imageRecordingDocker{DockerClient: newFakeEngine()}
	req := dockerMySQLRequest()
	req.DocumentDB = true
	if err := NewDockerMySQLProvisioner(docker).PrepareImages(context.Background(), req); err != nil || len(docker.images) != 0 {
		t.Fatalf("err=%v images=%v", err, docker.images)
	}
}

// A build that fails before the row records the container still removes it.
func TestDockerMySQL_DeprovisionUnderTheAdmittedNamespaceRemovesTheContainer(t *testing.T) {
	engine := newFakeEngine()
	provisionDockerMySQL(t, engine)
	prov := newTestDockerMySQL(engine)

	if err := prov.Deprovision(context.Background(), "org1-p1", "p1"); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}

	if status, _ := engine.ContainerStatus(context.Background(), "excalibase-p1-mysql"); status != containerNotFound {
		t.Fatalf("container is %s", status)
	}
}

func TestDockerPostgres_PreparesTheWatcherToo(t *testing.T) {
	docker := &imageRecordingDocker{}
	prov := &DockerPostgreSQLProvisioner{docker: docker}
	prov.SetWatcher(DockerWatcherConfig{Image: "watcher:pinned"})
	if err := prov.PrepareImages(context.Background(), domain.ProvisioningRequest{PostgresVersion: "17"}); err != nil {
		t.Fatalf("PrepareImages: %v", err)
	}
	if len(docker.images) != 2 || docker.images[1] != "watcher:pinned" {
		t.Fatalf("images %v", docker.images)
	}
}

// The container a row records goes too, under whatever name it has.
func TestDockerMySQL_DeprovisionAlsoRemovesTheRecordedContainer(t *testing.T) {
	engine := newFakeEngine()
	provisionDockerMySQL(t, engine)
	recorded, err := engine.CreateContainerSpec(context.Background(), ContainerSpec{Name: "recorded", Image: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.StartContainer(context.Background(), recorded); err != nil {
		t.Fatal(err)
	}

	if err := newTestDockerMySQL(engine).Deprovision(context.Background(), recorded, "p1"); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}

	if status, _ := engine.ContainerStatus(context.Background(), recorded); status != containerNotFound {
		t.Fatalf("recorded container is %s", status)
	}
}
