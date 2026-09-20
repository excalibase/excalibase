package k8s

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

// EXC-407/408: the CNPG Cluster takes its image from the catalogue, already
// resolved to a digest by the caller. The builder never assembles an image
// reference from a version string, because that would produce a floating tag.
func TestClusterUsesTheSuppliedDigestPinnedImage(t *testing.T) {
	image := "ghcr.io/excalibase/postgresql@sha256:" +
		"1111111111111111111111111111111111111111111111111111111111111111"
	obj := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "proj-1",
		Namespace: "ns",
		Tier:      config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
		ImageName: image,
	})
	spec := obj.Object["spec"].(map[string]interface{})
	if spec["imageName"] != image {
		t.Errorf("imageName: got %v, want %q", spec["imageName"], image)
	}
}

func TestClusterOmitsImageNameWhenNoneIsSupplied(t *testing.T) {
	obj := BuildPostgreSQLCluster(PostgreSQLClusterOpts{
		ProjectID: "proj-1",
		Namespace: "ns",
		Tier:      config.TierConfig{Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5"},
	})
	spec := obj.Object["spec"].(map[string]interface{})
	if _, present := spec["imageName"]; present {
		t.Error("imageName must be absent rather than defaulted")
	}
}
