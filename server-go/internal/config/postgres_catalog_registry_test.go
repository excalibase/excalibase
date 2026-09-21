package config

import (
	"os"
	"strings"
	"testing"
)

// publishWorkflow is the workflow that builds and pushes the images this
// catalogue pins, read from the repository so the two cannot drift.
const publishWorkflow = "../../../.github/workflows/postgres-image-publish.yml"

// postgresImageRepository is where we publish our own images. Docker Hub, the
// same place as every other Excalibase image: cd.yml pushes provisioning,
// studio and the rest there, and the AIO chart pulls them from there.
const postgresImageRepository = "excalibase/postgresql"

// EXC-430: images pinned to GHCR, which nothing pulls from, so every tenant
// pod met ImagePullBackOff.
func TestEveryPublishedImageIsOnTheRegistryWePublishTo(t *testing.T) {
	for _, entry := range postgresCatalog.Majors {
		if entry.Image == "" {
			continue
		}
		if !strings.HasPrefix(entry.Image, postgresImageRepository+"@") {
			t.Errorf("major %s: image %q is not %s; a tenant cannot pull it",
				entry.Major, entry.Image, postgresImageRepository)
		}
	}
}

// The workflow must push where the catalogue points.
func TestThePublishWorkflowPushesWhereTheCataloguePoints(t *testing.T) {
	body, err := os.ReadFile(publishWorkflow)
	if err != nil {
		t.Fatalf("read %s: %v", publishWorkflow, err)
	}
	workflow := string(body)

	if !strings.Contains(workflow, "tags: "+postgresImageRepository+":") {
		t.Errorf("the publish workflow does not push %s", postgresImageRepository)
	}
	if strings.Contains(workflow, "ghcr.io/excalibase/postgresql") {
		t.Error("the publish workflow still pushes or pins the GHCR image")
	}
}

// The upstream bases are public on ghcr.io and stay there; a "remove ghcr"
// sweep must not move them.
func TestUpstreamBasesStayOnTheirOwnRegistry(t *testing.T) {
	for _, entry := range postgresCatalog.Majors {
		if !strings.HasPrefix(entry.BaseImage, "ghcr.io/cloudnative-pg/postgresql@") {
			t.Errorf("major %s: baseImage %q is not the upstream CNPG image",
				entry.Major, entry.BaseImage)
		}
	}
	if !strings.HasPrefix(postgresCatalog.DocumentDBGatewayImage, "ghcr.io/documentdb/") {
		t.Errorf("gateway image %q is not the upstream DocumentDB one",
			postgresCatalog.DocumentDBGatewayImage)
	}
}
