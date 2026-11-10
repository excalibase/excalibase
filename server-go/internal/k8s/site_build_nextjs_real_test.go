//go:build integration

package k8s

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

// A repository that declares only next, react and react-dom, with its own wrangler date: no stubs, no planted
// node_modules/.bin. The real install adds the pinned OpenNext and wrangler, and the packed output must pass the
// deploy path's archive checks. Needs Docker and the network (the registry).
func TestNextjsPreset_BuildsAPlainRepoWithRealTools(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	work := t.TempDir()
	src := filepath.Join(work, "src")
	write(t, filepath.Join(src, "package.json"), `{"name":"plain","private":true,"scripts":{"build":"next build"},`+
		`"dependencies":{"next":"16.3.8","react":"19.2.0","react-dom":"19.2.0"}}`)
	write(t, filepath.Join(src, "app/layout.js"), `export default function Layout({ children }) { return <html><body>{children}</body></html>; }`)
	write(t, filepath.Join(src, "app/page.js"), `export default function Page() { return <h1>hello</h1>; }`)
	write(t, filepath.Join(src, "app/ssr/page.js"), `export const dynamic = "force-dynamic"; export default function Page() { return <p>{String(Date.now())}</p>; }`)
	write(t, filepath.Join(src, "wrangler.jsonc"), `{"name":"plain","compatibility_date":"2026-10-01","compatibility_flags":["nodejs_compat"]}`)
	scripts := scriptDir(t)
	if err := os.Chmod(work, 0o777); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("chmod", "-R", "a+rwX", work, scripts).CombinedOutput(); err != nil {
		t.Fatalf("chmod: %v %s", err, out)
	}
	run := exec.Command("docker", "run", "--rm", "-u", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "-e", "HOME=/work/home", "-e", "CI=true",
		"-e", "npm_config_cache=/work/npm-cache", "-e", "NEXT_TELEMETRY_DISABLED=1", "-e", "FRAMEWORK=nextjs", "-e", "ROOT_DIR=", "-e", "OUTPUT_DIR=",
		"-e", "HOLD_SECONDS=0", "-v", work+":/work", "-v", scripts+":/scripts:ro", "-w", "/work", sitebuilds.PinnedNodeImages["22"],
		"bash", "-c", "bash /scripts/build.sh; code=$?; echo BUILD_EXIT=$code; [ $code -eq 0 ] && bash /scripts/collect.sh; echo COLLECT_EXIT=$?")
	out, err := run.CombinedOutput()
	log := string(out)
	if err != nil || !strings.Contains(log, "BUILD_EXIT=0") || !strings.Contains(log, "COLLECT_EXIT=0") {
		t.Fatalf("docker run: %v\n%s", err, tail(log, 6000))
	}
	archive, err := os.Open(filepath.Join(work, "collect", "site.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	limits := sites.DefaultLimits
	limits.MaxServerBundleBytes = 30 << 20
	bundle, err := sites.ReadServerArchive(archive, limits, t.TempDir())
	if err != nil {
		t.Fatalf("the deploy path refused the packed output: %v", err)
	}
	defer bundle.Close()
	if bundle.Server.Main != "worker.js" || bundle.Server.CompatibilityDate != "2025-09-01" || !slices.Contains(bundle.Server.CompatibilityFlags, "nodejs_compat") {
		t.Errorf("server config %+v", bundle.Server)
	}
	if len(bundle.Modules) == 0 || len(bundle.Files) == 0 {
		t.Errorf("modules %d, assets %d", len(bundle.Modules), len(bundle.Files))
	}
}

func tail(text string, n int) string {
	if len(text) > n {
		return text[len(text)-n:]
	}
	return text
}
