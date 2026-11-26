package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// Every consumer of the container gate (a SetContainerGate method, or a
// ContainerGate field of an options struct) must be wired by the server. The
// walk reads the code's structure, not a string, so a consumer added later
// without its wiring fails here.
func parseGoFiles(t *testing.T, root string, keep func(path string) bool) []*ast.File {
	t.Helper()
	var files []*ast.File
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || !keep(path) {
			return err
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		files = append(files, parsed)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestEveryContainerGateConsumerIsWiredByTheServer(t *testing.T) {
	var setters, fields int
	for _, file := range parseGoFiles(t, filepath.Join("..", "..", "internal"), func(string) bool { return true }) {
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.FuncDecl:
				if n.Recv != nil && n.Name.Name == "SetContainerGate" {
					setters++
				}
			case *ast.StructType:
				for _, field := range n.Fields.List {
					for _, name := range field.Names {
						if name.Name == "ContainerGate" {
							fields++
						}
					}
				}
			}
			return true
		})
	}
	var calls, keyed int
	for _, file := range parseGoFiles(t, ".", func(string) bool { return true }) {
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "SetContainerGate" {
					calls++
				}
			case *ast.KeyValueExpr:
				if key, ok := n.Key.(*ast.Ident); ok && key.Name == "ContainerGate" {
					keyed++
				}
			}
			return true
		})
	}
	if setters == 0 || fields == 0 {
		t.Fatalf("found no consumers (setters=%d fields=%d): the walk is broken", setters, fields)
	}
	if calls < setters {
		t.Errorf("%d SetContainerGate methods but %d calls in cmd/server: a consumer is not wired", setters, calls)
	}
	if keyed < fields {
		t.Errorf("%d ContainerGate fields but %d keyed uses in cmd/server: a consumer is not wired", fields, keyed)
	}
}
