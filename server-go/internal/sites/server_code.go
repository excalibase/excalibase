package sites

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"path"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
)

// ServerCodeKept is how many of a site's latest succeeded server deploys keep
// their code for a rollback, within ServerCodeBudget; the live deploy's code
// and a deploy still running are kept beyond both.
const ServerCodeKept = 10

// ServerCodeBudget bounds the packed code one site keeps for rollbacks.
func ServerCodeBudget(limits Limits) int64 { return 3 * limits.MaxServerBundleBytes }

// maxPackedCodeBytes bounds what is read back of one deploy's kept code: the
// largest bundle a plan may allow, and a long-name header per module.
const maxPackedCodeBytes = 64<<20 + MaxServerModules*4*tarFramingBytes

// CodeStore keeps each server deploy's modules for the rollback window
// (ADR 0045 decision 7). Cloudflare keeps asset bodies by hash; nothing keeps code but this.
type CodeStore interface {
	// PutCode keeps the deploy's packed modules; the deploy is already recorded.
	PutCode(ctx context.Context, deploy *Deploy, packed []byte) error
	// GetCode answers ErrServerCodeGone once the deploy's code is no longer kept.
	GetCode(ctx context.Context, projectID, siteID, deployID string) ([]byte, error)
	// PruneCode keeps the code of the site's newest keep succeeded deploys
	// that fit in maxBytes together, its live one and any still pending, and drops the rest.
	PruneCode(ctx context.Context, projectID, siteID string, keep int, maxBytes int64) error
}

// packModules writes the bundle's modules as one tar.gz, named as the Worker loads them.
func packModules(bundle *Bundle) ([]byte, error) {
	var packed bytes.Buffer
	zipped := gzip.NewWriter(&packed)
	archive := tar.NewWriter(zipped)
	for _, mod := range bundle.Modules {
		if err := packModule(archive, bundle, mod); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("pack server code: %w", err)
	}
	if err := zipped.Close(); err != nil {
		return nil, fmt.Errorf("pack server code: %w", err)
	}
	return packed.Bytes(), nil
}

func packModule(archive *tar.Writer, bundle *Bundle, mod BundleFile) error {
	if err := archive.WriteHeader(&tar.Header{Name: mod.Path, Typeflag: tar.TypeReg, Mode: 0o644, Size: mod.Size}); err != nil {
		return fmt.Errorf("pack %s: %w", mod.Path, err)
	}
	content, err := bundle.Open(mod)
	if err != nil {
		return fmt.Errorf("pack %s: %w", mod.Path, err)
	}
	defer content.Close()
	if _, err := io.Copy(archive, content); err != nil {
		return fmt.Errorf("pack %s: %w", mod.Path, err)
	}
	return nil
}

// unpackModules reads kept code back as modules; it was checked when it was deployed.
func unpackModules(packed []byte) ([]cfworkers.Module, error) {
	unzipped, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		return nil, fmt.Errorf("read kept server code: %w", err)
	}
	archive := tar.NewReader(&cappedReader{source: unzipped, limit: maxPackedCodeBytes})
	modules := []cfworkers.Module{}
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return modules, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read kept server code: %w", err)
		}
		contentType, known := moduleTypes[path.Ext(header.Name)]
		if !cfworkers.ValidModuleName(header.Name) || !known || header.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("kept server code holds %q, which is not a module", header.Name)
		}
		body, err := io.ReadAll(archive)
		if err != nil {
			return nil, fmt.Errorf("read kept module %s: %w", header.Name, err)
		}
		modules = append(modules, cfworkers.Module{Name: header.Name, ContentType: contentType,
			Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }})
	}
}

// bundleModules streams the bundle's spooled modules into the script upload.
func bundleModules(bundle *Bundle) []cfworkers.Module {
	modules := make([]cfworkers.Module, 0, len(bundle.Modules))
	for _, mod := range bundle.Modules {
		modules = append(modules, cfworkers.Module{Name: mod.Path, ContentType: mod.ContentType,
			Open: func() (io.ReadCloser, error) { return bundle.Open(mod) }})
	}
	return modules
}
