// Command pgcatalog renders artefacts derived from the PostgreSQL image
// catalogue, so the build matrix and the Kubernetes object are both generated
// from the same file provisioning validates against.
//
//	pgcatalog            renders the CNPG ClusterImageCatalog
//	pgcatalog -matrix    renders the build matrix as JSON
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/excalibase/provisioning-poc/internal/config"
)

func main() {
	matrix := flag.Bool("matrix", false, "render the image build matrix as JSON")
	flag.Parse()

	out, err := render(*matrix)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgcatalog: %v\n", err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(out); err != nil {
		fmt.Fprintf(os.Stderr, "pgcatalog: write: %v\n", err)
		os.Exit(1)
	}
}

func render(matrix bool) ([]byte, error) {
	if matrix {
		return config.RenderBuildMatrix()
	}
	return config.RenderClusterImageCatalog()
}
