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
	const root = `import (
	"github.com/excalibase/provisioning-poc/internal/imagepull"
	"github.com/testcontainers/testcontainers-go"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
)
`
	cases := map[string]string{
		"aliased module Run":         "package x\nimport tcpg \"github.com/testcontainers/testcontainers-go/modules/postgres\"\nfunc start() { _, _ = tcpg.Run(nil, \"postgres:16\") }\n",
		"unaliased GenericContainer": "package x\nimport \"github.com/testcontainers/testcontainers-go\"\nfunc start() { _, _ = testcontainers.GenericContainer(nil, testcontainers.GenericContainerRequest{}) }\n",
		"unaliased Run":              "package x\nimport \"github.com/testcontainers/testcontainers-go\"\nfunc start() { _, _ = testcontainers.Run(nil, \"redis:7\") }\n",
		"second function unchecked":  "package x\n" + root + "func a() { _ = imagepull.EnsureLocal(nil, \"x\"); _, _ = tcpg.Run(nil, \"x\") }\nfunc b() { _, _ = tcpg.Run(nil, \"y\") }\nvar _ = testcontainers.Container(nil)\n",
	}
	for name, source := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "helper.go"), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		found, err := bareFiles(dir)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(found) != 1 {
			t.Errorf("%s: a planted bare start must be caught, got %v", name, found)
		}
	}
}

func TestGuardAcceptsAFunctionThatPrePulls(t *testing.T) {
	dir := t.TempDir()
	source := "package x\nimport (\n\"github.com/excalibase/provisioning-poc/internal/imagepull\"\n\"github.com/testcontainers/testcontainers-go\"\n)\nfunc start() { _ = imagepull.EnsureLocal(nil, \"x\"); _, _ = testcontainers.Run(nil, \"x\") }\n"
	if err := os.WriteFile(filepath.Join(dir, "ok.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if found, err := bareFiles(dir); err != nil || len(found) != 0 {
		t.Fatalf("a pre-pulling function must pass, got %v %v", found, err)
	}
}

// bareFiles lists the Go files under root with a function that starts a
// container with a testcontainers package (a module's Run, GenericContainer,
// RunContainer; the import alias does not matter) and does not itself call
// EnsureLocal. The check is per function, so one pre-pull cannot cover a
// start somewhere else in the file.
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
		for _, decl := range parsed.Decls {
			if function, ok := decl.(*ast.FuncDecl); ok && startsWithoutPrePull(function, aliases) {
				bare = append(bare, path+":"+function.Name.Name)
			}
		}
		return nil
	})
	return bare, err
}

func startsWithoutPrePull(function *ast.FuncDecl, aliases map[string]bool) bool {
	starts, prepulls := false, false
	ast.Inspect(function, func(node ast.Node) bool {
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
	return starts && !prepulls
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
		if name == "testcontainers-go" {
			name = "testcontainers" // the root package's name differs from its path
		}
		if spec.Name != nil {
			name = spec.Name.Name
		}
		aliases[name] = true
	}
	return aliases
}
