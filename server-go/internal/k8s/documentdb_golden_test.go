package k8s

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"sigs.k8s.io/yaml"
)

func TestDocumentDBClusterGolden(t *testing.T) {
	opts := documentDBOpts("owner_doc")
	opts.ImageName = "excalibase/postgresql@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	opts.DocumentDBGatewayImage = config.DocumentDBGatewayImage()
	encoded, err := yaml.Marshal(BuildPostgreSQLCluster(opts).Object)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	assertGoldenAt(t, "testdata/documentdb_cluster/owner-doc.yaml", string(encoded))
}
