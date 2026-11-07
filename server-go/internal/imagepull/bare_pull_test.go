package imagepull

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Go file that imports testcontainers pulls its images through a path that
// drops a failed pull, and the run fails later as "No such image". Every such
// file, helpers outside _test.go included and whatever alias the import has,
// must pre-pull through this package.
func TestEveryFileImportingTestcontainersPrePulls(t *testing.T) {
	bare, err := bareFiles("..")
	if err != nil {
		t.Fatal(err)
	}
	if len(bare) > 0 {
		t.Fatalf("these files start containers without imagepull.EnsureLocal: %v", bare)
	}
}

func TestGuardCatchesABareStart(t *testing.T) {
	dir := t.TempDir()
	bare := `package x

import tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"

func start() { _, _ = tcpg.Run(nil, "postgres:16") }
`
	if err := os.WriteFile(filepath.Join(dir, "helper.go"), []byte(bare), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := bareFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("a planted aliased bare start must be caught, got %v", found)
	}
}

// bareFiles lists the Go files under root that start a container with a
// testcontainers package (a module's Run, GenericContainer, RunContainer; the
// import alias does not matter) without calling EnsureLocal.
func bareFiles(root string) ([]string, error) {
	var bare []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.Contains(path, "/imagepull/") {
			return err
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		aliases := testcontainersAliases(parsed)
		if len(aliases) == 0 {
			return nil
		}
		starts, prepulls := false, false
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			qualifier, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch {
			case aliases[qualifier.Name] && (selector.Sel.Name == "Run" || selector.Sel.Name == "GenericContainer" || selector.Sel.Name == "RunContainer"):
				starts = true
			case qualifier.Name == "imagepull" && selector.Sel.Name == "EnsureLocal":
				prepulls = true
			}
			return true
		})
		if starts && !prepulls {
			bare = append(bare, path)
		}
		return nil
	})
	return bare, err
}

// testcontainersAliases is the names this file gives testcontainers imports.
func testcontainersAliases(parsed *ast.File) map[string]bool {
	aliases := map[string]bool{}
	for _, spec := range parsed.Imports {
		importPath := strings.Trim(spec.Path.Value, `"`)
		if !strings.HasPrefix(importPath, "github.com/testcontainers/testcontainers-go") {
			continue
		}
		name := filepath.Base(importPath)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		aliases[name] = true
	}
	return aliases
}
