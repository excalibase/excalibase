package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/appregistry"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/appregistrytest"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

const (
	testSiteBuildNamespace = "site-builds"
	testAppBuildNamespace  = "app-builds"
	testRuntimeClass       = "gvisor"
)

// gitBuildConfig is an install with Sites and site git builds on in a cluster; on names the dark features turned on.
func gitBuildConfig(t *testing.T, on ...features.Feature) config.AppConfig {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cfg := config.AppConfig{SitesEnabled: true, SitesGitBuildsEnabled: true, DarkFeatures: on, AppRuntimeClass: testRuntimeClass,
		StoragePath: t.TempDir(), StudioURL: "https://studio.example.com"}
	cfg.Sites = config.SitesConfig{AccountID: "a", APIToken: "t", DispatchNamespace: "n", BaseDomain: "example.app", HostsKVNamespaceID: "kv"}
	cfg.SiteGit = config.SiteGitConfig{AppID: 7, AppSlug: "excalibase", ClientID: "c", ClientSecret: "s", PrivateKeyPEM: string(keyPEM),
		WebhookSecret: "w", APIURL: "https://api.github.com", WebURL: "https://github.com", BuildNamespace: testSiteBuildNamespace, BuildSlots: 1}
	cfg.AppBuild = config.AppBuildConfig{Namespace: testAppBuildNamespace, SpoolPath: filepath.Join(t.TempDir(), "spool")}
	return cfg
}

// buildCluster holds the namespaces and RuntimeClass named; the chart makes them.
func buildCluster(namespaces ...string) *k8s.Client {
	objects := []runtime.Object{&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: testRuntimeClass}, Handler: "runsc"}}
	for _, name := range namespaces {
		objects = append(objects, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})
	}
	return k8s.NewClientFromInterfaces(fake.NewSimpleClientset(objects...), dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()))
}

func offlineStore(t *testing.T) dbOnlyStore {
	t.Helper()
	db, err := sql.Open("postgres", "postgres://unused.invalid/db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return dbOnlyStore{db: db}
}

func appBuildTestArgs(t *testing.T, cfg config.AppConfig, internal *appregistry.Internal, kube *k8s.Client) siteBuildArgs {
	t.Helper()
	store := offlineStore(t)
	cfg.AppRegistry.URL = "http://zot.invalid:5000"
	registry, err := newAppRegistryParts(cfg, store, fakestore.NewInstances(), internal)
	if err != nil {
		t.Fatal(err)
	}
	apps := apphost.NewPostgresAppStore(store.DB())
	return siteBuildArgs{cfg: cfg, sqlStore: store, kube: kube, apps: appBuildArgs{internal: internal, registry: registry, apps: apps, platformDB: store.DB()}}
}

// EXC-547 dark work: with appgitbuilds off, or without what an app build needs, nothing app-related is wired.
func TestAppBuilds_NothingWiredWhileTheFlagIsOff(t *testing.T) {
	cluster := buildCluster(testSiteBuildNamespace, testAppBuildNamespace)
	issuing := appregistrytest.New(t)
	siteGitOff := gitBuildConfig(t, features.AppGitBuilds)
	siteGitOff.SitesGitBuildsEnabled = false
	cases := map[string]struct {
		cfg      config.AppConfig
		internal *appregistry.Internal
		want     bool
	}{
		"flag off":            {gitBuildConfig(t), issuing, false},
		"site git builds off": {siteGitOff, issuing, false},
		"everything on":       {gitBuildConfig(t, features.AppGitBuilds), issuing, true},
	}
	for name, tc := range cases {
		builds := newSiteBuilds(appBuildTestArgs(t, tc.cfg, tc.internal, cluster))
		if builds.appsWired != tc.want {
			t.Errorf("%s: apps wired %v, want %v", name, builds.appsWired, tc.want)
		}
		if builds.apps == nil {
			t.Errorf("%s: the app git routes need their handler, wired or not", name)
		}
		if got := builds.dispatcher != nil && builds.dispatcher.AppsWired(); got != tc.want {
			t.Errorf("%s: dispatcher has an app runner and publisher %v, want %v", name, got, tc.want)
		}
		if builds.service == nil || builds.service.AppsWired() != tc.want {
			t.Errorf("%s: service app support wired, want %v", name, tc.want)
		}
	}
}

// The flag on where app builds should run but the registry does not issue stops the boot; the nightly,
// with site git builds off, and single-host keep answering that app builds are not offered.
func TestAppBuildsBlocked_FailsClosedOnlyWhereSiteBuildsRun(t *testing.T) {
	issuing := appregistrytest.New(t)
	guard, err := appregistry.NewInternal(appregistrytest.Host, nil)
	if err != nil {
		t.Fatal(err)
	}
	siteGitOff := gitBuildConfig(t, features.AppGitBuilds)
	siteGitOff.SitesGitBuildsEnabled = false
	singleHost := withMode(gitBuildConfig(t, features.AppGitBuilds), "docker")
	cases := map[string]struct {
		cfg      config.AppConfig
		internal *appregistry.Internal
		blocked  bool
	}{
		"a registry that only guards": {gitBuildConfig(t, features.AppGitBuilds), guard, true},
		"no registry":                 {gitBuildConfig(t, features.AppGitBuilds), nil, true},
		"flag off":                    {gitBuildConfig(t), nil, false},
		"site git builds off":         {siteGitOff, nil, false},
		"single-host":                 {singleHost, nil, false},
		"everything on":               {gitBuildConfig(t, features.AppGitBuilds), issuing, false},
	}
	for name, tc := range cases {
		if err := appBuildsBlocked(tc.cfg, tc.internal); (err != nil) != tc.blocked {
			t.Errorf("%s: %v, want blocked %v", name, err, tc.blocked)
		}
	}
}

// Wiring refuses what a publish could not run without: the registry's client and sweeper, and a spool path.
func TestEnableAppBuilds_RefusesWhatAPublishNeeds(t *testing.T) {
	cfg := gitBuildConfig(t, features.AppGitBuilds)
	internal := appregistrytest.New(t)
	args := appBuildTestArgs(t, cfg, internal, buildCluster(testSiteBuildNamespace, testAppBuildNamespace))
	cluster := buildCluster(testSiteBuildNamespace, testAppBuildNamespace)
	noRegistry := args.apps
	noRegistry.registry = appRegistryParts{}
	if err := enableAppBuilds(cfg, cluster, nil, nil, nil, noRegistry); err == nil {
		t.Error("no registry client or sweeper")
	}
	noSpool := cfg
	noSpool.AppBuild.SpoolPath = ""
	if err := enableAppBuilds(noSpool, cluster, nil, nil, nil, args.apps); err == nil {
		t.Error("no APP_BUILD_SPOOL_PATH")
	}
	// The publish lock lives in the platform database; without it two publishes could pass the quota on one measure.
	noLock := args.apps
	noLock.platformDB = nil
	if err := enableAppBuilds(cfg, cluster, nil, nil, nil, noLock); err == nil || !strings.Contains(err.Error(), "platform database") {
		t.Errorf("no platform database for the publish lock: %v", err)
	}
	// One namespace for both kinds would let each quota block the other: refused at boot, not at the first build.
	shared := cfg
	shared.SiteGit.BuildNamespace = shared.AppBuild.Namespace
	if err := enableAppBuilds(shared, cluster, nil, nil, nil, args.apps); err == nil || !strings.Contains(err.Error(), "own namespace") {
		t.Errorf("app builds in the site builds' namespace: %v", err)
	}
}

// App builds refuse to start without their own namespace and the sandbox RuntimeClass.
func TestVerifyAppBuildCluster(t *testing.T) {
	cfg := gitBuildConfig(t, features.AppGitBuilds)
	if err := verifyAppBuildCluster(cfg, buildCluster(testAppBuildNamespace)); err != nil {
		t.Fatalf("both present: %v", err)
	}
	if err := verifyAppBuildCluster(cfg, buildCluster(testSiteBuildNamespace)); err == nil {
		t.Error("no app build namespace")
	}
	noSandbox := cfg
	noSandbox.AppRuntimeClass = "runc-only"
	if err := verifyAppBuildCluster(noSandbox, buildCluster(testAppBuildNamespace)); err == nil {
		t.Error("no sandbox RuntimeClass")
	}
}

func TestAppBuildOptions_ShareTheSiteBuildsSlotsAndClass(t *testing.T) {
	cfg := gitBuildConfig(t, features.AppGitBuilds)
	cfg.SiteGit.PriorityClass, cfg.SiteGit.BuildSlots, cfg.SiteGit.PrepareImage = "builds-low", 2, "busybox@sha256:1"
	cfg.AppBuild.BuildKitImage, cfg.BuildEgressExtraDenyCIDRs = "buildkit@sha256:2", []string{"10.0.0.0/8"}
	options := appBuildOptions(cfg)
	if options.Namespace != testAppBuildNamespace || options.RuntimeClass != testRuntimeClass || options.PriorityClass != "builds-low" ||
		options.Slots != 2 || options.PrepareImage != "busybox@sha256:1" || options.BuildKitImage != "buildkit@sha256:2" ||
		options.ToolImage == "" || len(options.ExtraDenyCIDRs) != 1 {
		t.Fatalf("%+v", options)
	}
	// The runner keeps app builds out of the site builds' namespace, so it must be told which that is.
	if options.SiteBuildNamespace != cfg.SiteGit.BuildNamespace || options.Validate() != nil {
		t.Fatalf("site build namespace %q: %v", options.SiteBuildNamespace, options.Validate())
	}
}

// The spool is its own volume; a crashed publish's old leftovers are cleared at boot, and a path that is not a plain directory stops it.
func TestAppBuildSpool_OwnDirectorySweptOfOldLeftovers(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "spool")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	stale, fresh := filepath.Join(dir, "app-image-stale"), filepath.Join(dir, "app-image-fresh")
	for _, path := range []string{stale, fresh} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * appBuildSpoolMaxAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if got, err := appBuildSpool(dir); err != nil || got != dir {
		t.Fatalf("spool %s: %v", got, err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("spool mode %v: %v", info.Mode(), err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("an old leftover survived the boot")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a publish in flight lost its spool")
	}
	if made, err := appBuildSpool(filepath.Join(root, "made", "here")); err != nil || made == "" {
		t.Errorf("a missing spool is made: %v", err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"a file": file, "a symlink": link, "relative": "spool"} {
		if _, err := appBuildSpool(path); err == nil {
			t.Errorf("%s as the spool", name)
		}
	}
}

// The one app git route the flag never hides: a connection can always be removed.
func TestAppGitDisconnect_AnswersWithTheFlagOff(t *testing.T) {
	router, token := darkFeatureRouter(t)
	probe := darkProbe{http.MethodDelete, "/api/projects/" + matrixProjectA + "/apps/app-1/git"}
	if w := probeDark(router, probe, token); w.Body.String() == unmountedBody {
		t.Fatalf("the disconnect is unmounted with the flag off: %d", w.Code)
	}
}

// unreadableEntry is a spool entry whose details cannot be read, as when it vanishes mid-sweep.
type unreadableEntry struct{ fs.DirEntry }

func (unreadableEntry) Name() string               { return "app-image-gone" }
func (unreadableEntry) Info() (fs.FileInfo, error) { return nil, errors.New("lstat: no such file") }

// A spool entry the sweep cannot read is named in a WARN, never skipped silently.
func TestSweepSpoolEntry_WarnsWhenAnEntryCannotBeRead(t *testing.T) {
	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })
	sweepSpoolEntry(t.TempDir(), unreadableEntry{}, time.Now())
	if line := logged.String(); !strings.Contains(line, "WARN: app build spool") || !strings.Contains(line, "app-image-gone") || !strings.Contains(line, "no such file") {
		t.Errorf("logged %q", line)
	}
}

// Builds run untrusted code and keep the stricter list; the list for running apps never reaches them, nor theirs the apps.
func TestBuildsAndAppsReadTheirOwnDenyLists(t *testing.T) {
	builds, apps := []string{"203.0.113.1/32"}, []string{"198.51.100.1/32"}
	both := gitBuildConfig(t, features.AppGitBuilds)
	both.BuildEgressExtraDenyCIDRs, both.AppEgressExtraDenyCIDRs = builds, apps
	onlyApps := gitBuildConfig(t, features.AppGitBuilds)
	onlyApps.AppEgressExtraDenyCIDRs = apps

	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"app builds", appBuildOptions(both).ExtraDenyCIDRs, builds},
		{"site builds", siteBuildOptions(both).ExtraDenyCIDRs, builds},
		{"site builds on a single host", singleHostBuildOptions(both, false).ExtraDenyCIDRs, builds},
		{"running apps", appRenderOptions(both).ExtraDenyCIDRs, apps},
		{"app builds never fall back to the apps' list", appBuildOptions(onlyApps).ExtraDenyCIDRs, nil},
		{"site builds never fall back to the apps' list", siteBuildOptions(onlyApps).ExtraDenyCIDRs, nil},
		{"single-host builds never fall back to the apps' list", singleHostBuildOptions(onlyApps, false).ExtraDenyCIDRs, nil},
	}
	for _, tc := range cases {
		if !slices.Equal(tc.got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

// The apps' list set and the builds' not is what an upgrade leaves: the boot stops rather than leave builds unlisted.
func TestSiteBuildRunner_RefusesTheAppsListWithoutTheBuilds(t *testing.T) {
	apps, builds := []string{"198.51.100.1/32"}, []string{"203.0.113.1/32"}
	cases := []struct {
		name         string
		apps, builds []string
		refused      bool
	}{{"only the apps'", apps, nil, true}, {"both", apps, builds, false}, {"only the builds'", nil, builds, false}, {"neither", nil, nil, false}}
	for _, tc := range cases {
		cfg := config.AppConfig{AppEgressExtraDenyCIDRs: tc.apps, BuildEgressExtraDenyCIDRs: tc.builds}
		_, err := siteBuildRunner(siteBuildArgs{cfg: cfg})
		if got := err != nil && strings.Contains(err.Error(), "BUILD_EGRESS_EXTRA_DENY_CIDRS"); got != tc.refused {
			t.Errorf("%s: refused %v (%v), want %v", tc.name, got, err, tc.refused)
		}
	}
}

// The alert webhook is not a build: it keeps the apps' list and never reads the builds'.
func TestAlertWebhookReadsTheAppsDenyList(t *testing.T) {
	cfg := config.AppConfig{AppEgressExtraDenyCIDRs: []string{"198.51.100.1/32"}, BuildEgressExtraDenyCIDRs: []string{"203.0.113.1/32"}}
	got := alertWebhookDenyList(cfg)
	if len(got) != 1 || got[0].String() != "198.51.100.1/32" {
		t.Fatalf("alert webhook denies %v, want the apps' list only", got)
	}
}
