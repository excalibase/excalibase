package trust

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// trust decides what an account may run; it must never reach what acts on an
// account (suspension, app control, k8s, handlers). Only the standard library
// and these two packages may be imported, so a finding can never become an action.
var allowedModuleImports = map[string]bool{
	"github.com/excalibase/provisioning-poc/internal/domain":   true,
	"github.com/excalibase/provisioning-poc/internal/features": true,
}

func TestPackageImportsOnlyDomainAndFeatures(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no sources: %v", err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range parsed.Imports {
			path := strings.Trim(spec.Path.Value, `"`)
			if strings.Contains(path, ".") && !allowedModuleImports[path] {
				t.Errorf("%s imports %s; trust may import only domain and features", file, path)
			}
		}
	}
}

// The trust tables are read and written through pg_user_trust.go and nowhere else.
func TestTrustTablesAreTouchedOnlyByTheStore(t *testing.T) {
	root := filepath.Join("..", "..")
	owner := filepath.Join("internal", "storage", "postgres", "pg_user_trust.go")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rel == owner {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, table := range []string{"user_trust", "trust_audit"} {
			if strings.Contains(string(body), table) {
				t.Errorf("%s names the table %s; only %s may", rel, table, owner)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
