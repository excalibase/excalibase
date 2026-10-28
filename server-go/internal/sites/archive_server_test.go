package sites_test

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

const nextSiteJSON = `{"kind":"server","main":"worker.js","compatibilityDate":"2025-09-01","compatibilityFlags":["nodejs_compat"],"framework":"nextjs"}`

func readServerArchive(t *testing.T, raw []byte, limits sites.Limits) (*sites.Bundle, error) {
	t.Helper()
	bundle, err := sites.ReadServerArchive(bytes.NewReader(raw), limits, t.TempDir())
	if bundle != nil {
		t.Cleanup(func() { _ = bundle.Close() })
	}
	return bundle, err
}

// openNextEntries is an OpenNext build packed in the server layout.
func openNextEntries(siteJSON string) []sitestest.Entry {
	return []sitestest.Entry{
		sitestest.Dir("./"),
		sitestest.File("./site.json", siteJSON),
		sitestest.Dir("./server/"),
		sitestest.File("./server/worker.js", "export default { fetch() {} }"),
		sitestest.File("./server/worker.js.map", `{"version":3}`),
		sitestest.Dir("./assets/"),
		sitestest.File("./assets/BUILD_ID", "L-2K"),
		sitestest.File("./assets/_next/static/chunk.js", "console.log(1)"),
		sitestest.File("./assets/cdn-cgi/_next_cache/L-2K/$/index.cache", "<html>"),
	}
}

func serverArchive(t *testing.T, siteJSON string, extra ...sitestest.Entry) []byte {
	t.Helper()
	return sitestest.Gzip(t, sitestest.Tar(t, append(openNextEntries(siteJSON), extra...)...))
}

func TestServerArchiveSplitsModulesFromPublicAssets(t *testing.T) {
	bundle, err := readServerArchive(t, serverArchive(t, nextSiteJSON), sites.DefaultLimits)
	if err != nil {
		t.Fatalf("ReadServerArchive: %v", err)
	}
	manifest := bundle.Manifest()
	for _, path := range []string{"/BUILD_ID", "/_next/static/chunk.js", "/cdn-cgi/_next_cache/L-2K/$/index.cache"} {
		if _, ok := manifest[path]; !ok {
			t.Errorf("manifest lacks %s: %v", path, manifest)
		}
	}
	for path := range manifest {
		if strings.Contains(path, "worker") || strings.HasPrefix(path, "/server") || path == "/site.json" {
			t.Errorf("server code or config is a public file: %s", path)
		}
	}
	names := []string{}
	for _, mod := range bundle.Modules {
		names = append(names, mod.Path+" "+mod.ContentType)
	}
	if strings.Join(names, ",") != "worker.js application/javascript+module,worker.js.map application/source-map" {
		t.Fatalf("modules = %v", names)
	}
	server := bundle.Server
	if server == nil || server.Main != "worker.js" || server.CompatibilityDate != "2025-09-01" ||
		strings.Join(server.CompatibilityFlags, ",") != "nodejs_compat" || server.Framework != "nextjs" {
		t.Fatalf("server = %+v", server)
	}
	if bundle.CodeBytes != int64(len("export default { fetch() {} }")) {
		t.Errorf("code bytes = %d; a source map is not code", bundle.CodeBytes)
	}
}

func TestServerArchiveDefaultsTheCompatibilityDate(t *testing.T) {
	bundle, err := readServerArchive(t, serverArchive(t, `{"kind":"server","main":"worker.js"}`), sites.DefaultLimits)
	if err != nil {
		t.Fatalf("ReadServerArchive: %v", err)
	}
	if bundle.Server.CompatibilityDate != sites.DefaultCompatibilityDate || len(bundle.Server.CompatibilityFlags) != 0 {
		t.Fatalf("server = %+v", bundle.Server)
	}
}

func TestServerArchiveRefusesABadSiteJSON(t *testing.T) {
	cases := map[string]struct{ siteJSON, fragment string }{
		"not json":          {`{"kind":`, "site.json"},
		"unknown field":     {`{"kind":"server","main":"worker.js","bindings":[]}`, "bindings"},
		"static kind":       {`{"kind":"static","main":"worker.js"}`, `"kind": "server"`},
		"no main":           {`{"kind":"server"}`, "main"},
		"main not uploaded": {`{"kind":"server","main":"index.js"}`, "index.js"},
		"main is a map":     {`{"kind":"server","main":"worker.js.map"}`, "worker.js.map"},
		"date format":       {`{"kind":"server","main":"worker.js","compatibilityDate":"2025/09/01"}`, "YYYY-MM-DD"},
		"date too old":      {`{"kind":"server","main":"worker.js","compatibilityDate":"2024-09-22"}`, "2024-09-23"},
		"date untested":     {`{"kind":"server","main":"worker.js","compatibilityDate":"2099-01-01"}`, sites.LatestCompatibilityDate},
		"unknown flag":      {`{"kind":"server","main":"worker.js","compatibilityFlags":["nodejs_compat","unsafe_eval"]}`, "unsafe_eval"},
		"repeated flag":     {`{"kind":"server","main":"worker.js","compatibilityFlags":["nodejs_compat","nodejs_compat"]}`, "twice"},
		"unknown framework": {`{"kind":"server","main":"worker.js","framework":"rails"}`, "rails"},
		"trailing data":     {`{"kind":"server","main":"worker.js"} {}`, "site.json"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := readServerArchive(t, serverArchive(t, tc.siteJSON), sites.DefaultLimits)
			wantArchiveError(t, err, tc.fragment)
		})
	}
}

func TestServerArchiveRefusesWhatBreaksTheLayout(t *testing.T) {
	cases := map[string]struct {
		entries  []sitestest.Entry
		fragment string
	}{
		"no site.json": {[]sitestest.Entry{sitestest.File("./server/worker.js", "x"), sitestest.File("./assets/a.txt", "a")}, "site.json"},
		"no assets": {[]sitestest.Entry{sitestest.File("./site.json", nextSiteJSON), sitestest.File("./server/worker.js", "x")},
			"assets/"},
		"file at the root":        {append(openNextEntries(nextSiteJSON), sitestest.File("./index.html", "<p>")), "index.html"},
		"other folder":            {append(openNextEntries(nextSiteJSON), sitestest.File("./.open-next/worker.js", "x")), ".open-next"},
		"worker in the assets":    {append(openNextEntries(nextSiteJSON), sitestest.File("./assets/_worker.js", "x")), "_worker.js"},
		"worker dir in assets":    {append(openNextEntries(nextSiteJSON), sitestest.File("./assets/_worker.js/index.js", "x")), "_worker.js"},
		"module copied to assets": {append(openNextEntries(nextSiteJSON), sitestest.File("./assets/w.js", "export default { fetch() {} }")), "server/worker.js"},
		"unknown module type":     {append(openNextEntries(nextSiteJSON), sitestest.File("./server/run.py", "x")), "run.py"},
		"site.json twice":         {append(openNextEntries(nextSiteJSON), sitestest.File("site.json", nextSiteJSON)), "twice"},
		"oversized site.json": {append(openNextEntries(nextSiteJSON)[:1], sitestest.File("./site.json", strings.Repeat(" ", 17<<10)+nextSiteJSON)),
			"site.json"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := readServerArchive(t, sitestest.Gzip(t, sitestest.Tar(t, tc.entries...)), sites.DefaultLimits)
			wantArchiveError(t, err, tc.fragment)
		})
	}
}

func TestServerArchiveHoldsCodeToThePlansBundleSize(t *testing.T) {
	limits := sites.DefaultLimits
	limits.MaxServerBundleBytes = 64
	big := sitestest.File("./server/chunk.js", strings.Repeat("a", 40))
	_, err := readServerArchive(t, serverArchive(t, nextSiteJSON, big), limits)
	wantArchiveError(t, err, "server code")
	var archiveErr *sites.ArchiveError
	if !errors.As(err, &archiveErr) || !archiveErr.TooLarge {
		t.Fatalf("err = %v, want it marked too large", err)
	}
	// Source maps have their own allowance of the same size, so a 3 MiB worker with a 7 MiB map fits a 10 MiB plan.
	maps := sitestest.File("./server/chunk.js.map", strings.Repeat("m", 48))
	if _, err := readServerArchive(t, serverArchive(t, nextSiteJSON, maps), limits); err != nil {
		t.Fatalf("a map inside its own allowance: %v", err)
	}
}

func TestServerArchiveCapsTheModuleCount(t *testing.T) {
	extra := []sitestest.Entry{}
	for i := range sites.MaxServerModules {
		extra = append(extra, sitestest.File("./server/c"+strconv.Itoa(i)+".js", "1"))
	}
	_, err := readServerArchive(t, serverArchive(t, nextSiteJSON, extra...), sites.DefaultLimits)
	wantArchiveError(t, err, "modules")
}

func TestServerArchiveFitsTheUploadCeiling(t *testing.T) {
	limits := sites.DefaultLimits
	if sites.MaxServerArchiveBytes(limits) < sites.MaxArchiveBytes(limits)+2*limits.MaxServerBundleBytes {
		t.Fatal("the server archive ceiling leaves no room for the code and its maps")
	}
}
