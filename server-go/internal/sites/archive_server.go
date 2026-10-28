package sites

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
)

// MaxServerModules bounds the code files of one server deploy; OpenNext's bundle is one module and its map.
const MaxServerModules = 200

// moduleTypes are the code files a server deploy may hold, by extension.
var moduleTypes = map[string]string{
	".js":   cfworkers.ModuleJavaScript,
	".mjs":  cfworkers.ModuleJavaScript,
	".wasm": cfworkers.ModuleWasm,
	".txt":  cfworkers.ModuleText,
	".bin":  cfworkers.ModuleData,
}

const serverLayoutHint = "pack the build as server/ (code), assets/ (public files) and site.json"

// MaxServerArchiveBytes bounds a server deploy's archive as read: the
// assets, the code, site.json and framing.
func MaxServerArchiveBytes(limits Limits) int64 {
	return MaxArchiveBytes(limits) + limits.MaxServerBundleBytes + int64(2*MaxServerModules+4)*tarFramingBytes + maxSiteJSONBytes
}

// ReadServerArchive streams a server deploy (EXC-661) into a spool under
// spoolDir: server/ becomes the Worker's modules, assets/ its public files,
// and site.json its runtime settings. Server code never enters the manifest.
func ReadServerArchive(body io.Reader, limits Limits, spoolDir string) (*Bundle, error) {
	dir, err := os.MkdirTemp(spoolDir, "site-deploy-")
	if err != nil {
		return nil, fmt.Errorf("create deploy spool: %w", err)
	}
	bundle := &Bundle{dir: dir}
	if err := bundle.fillServer(body, limits); err != nil {
		if cleanupErr := bundle.Close(); cleanupErr != nil {
			return nil, errors.Join(err, cleanupErr)
		}
		return nil, err
	}
	return bundle, nil
}

func (b *Bundle) fillServer(body io.Reader, limits Limits) error {
	assets, modules := map[string]bool{}, map[string]bool{}
	maxEntries := 2*limits.MaxFiles + 2*MaxServerModules + 4
	err := readEntries(body, MaxServerArchiveBytes(limits), maxEntries, func(reader io.Reader, header *tar.Header) error {
		return b.addServerEntry(reader, header, limits, assets, modules)
	})
	if err != nil {
		return err
	}
	return b.checkServerLayout()
}

func (b *Bundle) addServerEntry(reader io.Reader, header *tar.Header, limits Limits, assets, modules map[string]bool) error {
	if err := refuseEntryType(header); err != nil || header.Typeflag == tar.TypeXGlobalHeader {
		return err
	}
	isDir := header.Typeflag == tar.TypeDir
	sitePath, err := cleanPath(header.Name, isDir)
	if err != nil || sitePath == "" {
		return err
	}
	top, rest, _ := strings.Cut(strings.TrimPrefix(sitePath, "/"), "/")
	switch {
	case top == "site.json" && rest == "" && !isDir:
		return b.readSiteJSON(reader, header.Size)
	case (top == "server" || top == "assets") && isDir:
		return nil
	case top == "assets" && rest != "":
		return b.addServerAsset(reader, "/"+rest, header.Size, limits, assets)
	case top == "server" && rest != "":
		return b.addModule(reader, rest, header.Size, limits, modules)
	default:
		return refuse("%s is outside the server layout; %s", sitePath, serverLayoutHint)
	}
}

func (b *Bundle) readSiteJSON(reader io.Reader, size int64) error {
	if b.Server != nil {
		return refuse("site.json appears twice in the archive")
	}
	config, err := parseSiteJSON(reader, size)
	if err != nil {
		return err
	}
	b.Server = config
	return nil
}

func (b *Bundle) addServerAsset(reader io.Reader, publicPath string, size int64, limits Limits, seen map[string]bool) error {
	// Pages-style adapters put the server in _worker.js inside the output folder; served, it would be public code.
	if lower := strings.ToLower(publicPath); lower == "/_worker.js" || strings.HasPrefix(lower, "/_worker.js/") {
		return refuse("assets%s would publish server code as a public file; move it under server/", publicPath)
	}
	return b.addFile(reader, publicPath, size, limits, seen)
}

func (b *Bundle) addModule(reader io.Reader, name string, size int64, limits Limits, seen map[string]bool) error {
	if path.Ext(name) == ".map" {
		// Kept for every rollback and as large again as the code; stack traces stay unmapped in v1.
		return refuse("server/%s is a source map, which is not uploaded; delete server/*.map before packing", name)
	}
	contentType, ok := moduleTypes[path.Ext(name)]
	if !ok {
		return refuse("server/%s is not a file a Worker runs; server/ holds .js, .mjs, .wasm, .txt or .bin files", name)
	}
	if !cfworkers.ValidModuleName(name) {
		return refuse("server/%s has a name a Worker module may not have; use letters, digits and . _ - / @ +", name)
	}
	if seen[name] {
		return refuse("server/%s appears twice in the archive", name)
	}
	seen[name] = true
	if len(b.Modules) >= MaxServerModules {
		return refuseSize("server/ holds more than %d modules", MaxServerModules)
	}
	if err := b.reserveCode(size, limits); err != nil {
		return err
	}
	file, err := b.spool(reader, "/server/"+name, size)
	if err != nil {
		return err
	}
	file.Path, file.ContentType = name, contentType
	b.Modules = append(b.Modules, file)
	return nil
}

// reserveCode counts a module against the plan's bundle size.
func (b *Bundle) reserveCode(size int64, limits Limits) error {
	if b.CodeBytes+size > limits.MaxServerBundleBytes {
		return refuseSize("the server code is larger than this plan's %d bytes uncompressed; deploy the app as a container instead", limits.MaxServerBundleBytes)
	}
	b.CodeBytes += size
	return nil
}

func (b *Bundle) checkServerLayout() error {
	if b.Server == nil {
		return refuse("the archive has no site.json at its root; %s", serverLayoutHint)
	}
	if len(b.Files) == 0 {
		return refuse("the archive has no files under assets/; %s", serverLayoutHint)
	}
	if !b.hasJavaScriptModule(b.Server.Main) {
		return refuse("site.json names main module %q, which is not a .js or .mjs file under server/", b.Server.Main)
	}
	code := map[[32]byte]string{}
	for _, mod := range b.Modules {
		code[mod.digest] = mod.Path
	}
	for _, file := range b.Files {
		if name, copied := code[file.digest]; copied {
			return refuse("assets%s is a copy of server/%s; server code is never a public file", file.Path, name)
		}
	}
	return nil
}

func (b *Bundle) hasJavaScriptModule(name string) bool {
	for _, mod := range b.Modules {
		if mod.Path == name {
			return mod.ContentType == cfworkers.ModuleJavaScript
		}
	}
	return false
}
