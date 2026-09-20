// Command pgcatalog renders the CNPG ClusterImageCatalog for the supported
// PostgreSQL majors onto stdout. The publish workflow runs it after pushing
// the images so the Kubernetes object is always derived from the same
// catalogue provisioning validates against.
package main

import (
	"fmt"
	"os"

	"github.com/excalibase/provisioning-poc/internal/config"
)

func main() {
	out, err := config.RenderClusterImageCatalog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgcatalog: %v\n", err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(out); err != nil {
		fmt.Fprintf(os.Stderr, "pgcatalog: write: %v\n", err)
		os.Exit(1)
	}
}
