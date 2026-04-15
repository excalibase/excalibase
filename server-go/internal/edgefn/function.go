package edgefn

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	esbuild "github.com/evanw/esbuild/pkg/api"
)

// MaxCodeSize caps the bundled code at 512 KB (Deno runtime agrees).
const MaxCodeSize = 512 * 1024

// MaxFileCount caps files per function.
const MaxFileCount = 50

// validIDPattern — function IDs: 1-64 lowercase alphanumeric/hyphen/underscore.
var validIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_\-]{0,63}$`)

// validPathPattern — file paths inside a function: relative, no traversal,
// lowercase letters, digits, hyphen, underscore, forward slash, dot.
// Max 128 chars. Used for index.ts, utils.ts, _shared/cors.ts, etc.
var validPathPattern = regexp.MustCompile(`^[a-z0-9_][a-z0-9_\-./]{0,127}$`)

// ValidateID checks the function id is safe for filesystem, URL, and runtime use.
func ValidateID(id string) error {
	if !validIDPattern.MatchString(id) {
		return fmt.Errorf("invalid function id: must be 1-64 lowercase alphanumeric/hyphen/underscore starting with a letter or digit")
	}
	return nil
}

// ValidateProjectID reuses the same rules the platform generates (proj_<10 chars>),
// but is permissive enough to accept "default" or UUID-style ids too.
func ValidateProjectID(pid string) error {
	return validateProjectID(pid)
}

func validateProjectID(pid string) error {
	if pid == "" {
		return fmt.Errorf("project id is required")
	}
	if len(pid) > 64 {
		return fmt.Errorf("project id must be 64 characters or fewer")
	}
	if strings.ContainsAny(pid, "/\\..") {
		return fmt.Errorf("project id contains invalid characters")
	}
	return nil
}

// File is a single source file belonging to a function. Entry point is "index.ts".
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Function is a per-project edge function — Supabase-style, multi-file, HTTP handler.
type Function struct {
	ID          string `json:"id"`        // slug, unique per project
	ProjectID   string `json:"projectId"` // opaque project ref (e.g. proj_a3k9fx)
	Name        string `json:"name"`      // display name
	Description string `json:"description,omitempty"`
	Files       []File `json:"files"` // must contain index.ts
	// VerifyJwt: nil/missing → true (safe default).
	// Set to *false to opt out — public route lets unauthenticated traffic through
	// straight to the user handler. Use only for webhooks that do their own auth.
	VerifyJwt *bool     `json:"verifyJwt,omitempty"`
	Active    bool      `json:"active"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// JwtVerificationRequired returns true unless the function has explicitly opted
// out via VerifyJwt=false. Default-on is the safer choice so that newly created
// functions are not accidentally exposed without auth.
func (f *Function) JwtVerificationRequired() bool {
	if f.VerifyJwt == nil {
		return true
	}
	return *f.VerifyJwt
}

// Validate checks the function is structurally sound. Also bundles files to
// confirm the bundle fits within MaxCodeSize before the function is persisted.
func (f *Function) Validate() error {
	if err := validateProjectID(f.ProjectID); err != nil {
		return err
	}
	if err := ValidateID(f.ID); err != nil {
		return err
	}
	if f.Name == "" || len(f.Name) > 200 {
		return fmt.Errorf("name must be 1-200 characters")
	}
	if len(f.Files) == 0 {
		return fmt.Errorf("function must have at least one of its files (index.ts required)")
	}
	if len(f.Files) > MaxFileCount {
		return fmt.Errorf("function has too many files (max %d)", MaxFileCount)
	}

	hasIndex := false
	for _, file := range f.Files {
		if !validPathPattern.MatchString(file.Path) {
			return fmt.Errorf("invalid file path %q: only lowercase alphanumerics, _, -, /, . allowed", file.Path)
		}
		if strings.Contains(file.Path, "..") {
			return fmt.Errorf("invalid file path %q: path traversal not allowed", file.Path)
		}
		if file.Path == "index.ts" {
			hasIndex = true
		}
		if len(file.Content) == 0 {
			return fmt.Errorf("file %q is empty", file.Path)
		}
	}
	if !hasIndex {
		return fmt.Errorf("function must contain an index.ts entry point")
	}

	if _, err := f.Bundle(); err != nil {
		return err
	}
	return nil
}

// RuntimeID is the id under which this function is registered in the shared
// Deno runtime. Prefixing with the project ref prevents cross-project collisions.
func (f *Function) RuntimeID() string {
	return f.ProjectID + "__" + f.ID
}

// Bundle produces the JS source shipped to the Deno runtime. Uses esbuild
// with a virtual filesystem plugin so the user's source files never touch
// disk. Entry point is always index.ts. Output format is IIFE assigned to
// a global, then a trailer assigns the default export to
// globalThis.__excalibase_default — which is the contract the worker
// template in deno-server/server.ts reads from.
//
// External imports (npm:, jsr:, node:, http:, https:) pass through to the
// Deno runtime unchanged — Deno resolves and fetches them at worker start.
// Relative imports (./utils.ts, ../shared/cors.ts) are resolved from the
// virtual filesystem provided in f.Files.
func (f *Function) Bundle() (string, error) {
	virtualFiles := make(map[string]string, len(f.Files))
	hasIndex := false
	for _, file := range f.Files {
		virtualFiles[file.Path] = file.Content
		if file.Path == "index.ts" {
			hasIndex = true
		}
	}
	if !hasIndex {
		return "", fmt.Errorf("function must contain an index.ts entry point")
	}

	result := esbuild.Build(esbuild.BuildOptions{
		EntryPoints: []string{"index.ts"},
		Bundle:      true,
		Format:      esbuild.FormatIIFE,
		GlobalName:  "__excalibase_bundle",
		Target:      esbuild.ES2022,
		Platform:    esbuild.PlatformNeutral,
		Write:       false,
		LogLevel:    esbuild.LogLevelSilent,
		Plugins:     []esbuild.Plugin{virtualFSPlugin(virtualFiles)},
	})
	if len(result.Errors) > 0 {
		return "", fmt.Errorf("bundle error: %s", result.Errors[0].Text)
	}
	if len(result.OutputFiles) == 0 {
		return "", fmt.Errorf("bundle produced no output")
	}

	// Append the handler hoist. esbuild IIFE with GlobalName emits:
	//   var __excalibase_bundle = (() => { ... return index_exports; })();
	// where `index_exports.default` is the user's default export.
	bundled := string(result.OutputFiles[0].Contents)
	final := bundled + "\nglobalThis.__excalibase_default = __excalibase_bundle && __excalibase_bundle.default;\n"

	if len(final) > MaxCodeSize {
		return "", fmt.Errorf("bundled code exceeds maximum size (%d KB)", MaxCodeSize/1024)
	}
	return final, nil
}

// virtualFSPlugin returns an esbuild plugin that resolves and loads modules
// from an in-memory file map. External protocols (npm:, jsr:, node:, http://,
// https://) pass through as external so Deno resolves them at worker start.
func virtualFSPlugin(files map[string]string) esbuild.Plugin {
	return esbuild.Plugin{
		Name: "excalibase-virtual-fs",
		Setup: func(build esbuild.PluginBuild) {
			build.OnResolve(esbuild.OnResolveOptions{Filter: `.*`},
				func(args esbuild.OnResolveArgs) (esbuild.OnResolveResult, error) {
					// Let Deno handle remote and runtime modules.
					if isExternal(args.Path) {
						return esbuild.OnResolveResult{Path: args.Path, External: true}, nil
					}

					resolved := args.Path
					if args.Importer != "" && (strings.HasPrefix(args.Path, "./") || strings.HasPrefix(args.Path, "../")) {
						importerDir := path.Dir(args.Importer)
						resolved = path.Clean(path.Join(importerDir, args.Path))
					}
					resolved = strings.TrimPrefix(resolved, "./")

					if _, ok := files[resolved]; ok {
						return esbuild.OnResolveResult{Path: resolved, Namespace: "virtual"}, nil
					}
					return esbuild.OnResolveResult{}, fmt.Errorf("module %q not found in function bundle (importer=%q)", args.Path, args.Importer)
				})

			build.OnLoad(esbuild.OnLoadOptions{Filter: `.*`, Namespace: "virtual"},
				func(args esbuild.OnLoadArgs) (esbuild.OnLoadResult, error) {
					contents, ok := files[args.Path]
					if !ok {
						return esbuild.OnLoadResult{}, fmt.Errorf("file %q not in virtual fs", args.Path)
					}
					loader := loaderForExt(args.Path)
					return esbuild.OnLoadResult{
						Contents: &contents,
						Loader:   loader,
					}, nil
				})
		},
	}
}

func isExternal(p string) bool {
	return strings.HasPrefix(p, "npm:") ||
		strings.HasPrefix(p, "jsr:") ||
		strings.HasPrefix(p, "node:") ||
		strings.HasPrefix(p, "http://") ||
		strings.HasPrefix(p, "https://")
}

func loaderForExt(p string) esbuild.Loader {
	switch {
	case strings.HasSuffix(p, ".tsx"):
		return esbuild.LoaderTSX
	case strings.HasSuffix(p, ".jsx"):
		return esbuild.LoaderJSX
	case strings.HasSuffix(p, ".js"), strings.HasSuffix(p, ".mjs"):
		return esbuild.LoaderJS
	default:
		return esbuild.LoaderTS
	}
}
