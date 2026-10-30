package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
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
	cfg.AppBuild = config.AppBuildConfig{Namespace: testAppBuildNamespace}
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
	return siteBuildArgs{cfg: cfg, sqlStore: store, kube: kube, apps: appBuildArgs{internal: internal, registry: registry, apps: apps}}
}

// EXC-547 dark work: with appgitbuilds off, or without what an app build needs, nothing app-related is wired.
func TestAppBuilds_NothingWiredWhileTheFlagIsOff(t *testing.T) {
	cluster := buildCluster(testSiteBuildNamespace, testAppBuildNamespace)
	issuing := appregistrytest.New(t)
	guard, err := appregistry.NewInternal(appregistrytest.Host, nil)
	if err != nil {
		t.Fatal(err)
	}
	siteGitOff := gitBuildConfig(t, features.AppGitBuilds)
	siteGitOff.SitesGitBuildsEnabled = false
	cases := map[string]struct {
		cfg      config.AppConfig
		internal *appregistry.Internal
		want     bool
	}{
		"flag off":                    {gitBuildConfig(t), issuing, false},
		"a registry that only guards": {gitBuildConfig(t, features.AppGitBuilds), guard, false},
		"no registry":                 {gitBuildConfig(t, features.AppGitBuilds), nil, false},
		"site git builds off":         {siteGitOff, issuing, false},
		"everything on":               {gitBuildConfig(t, features.AppGitBuilds), issuing, true},
	}
	for name, tc := range cases {
		builds := newSiteBuilds(appBuildTestArgs(t, tc.cfg, tc.internal, cluster))
		if builds.appsWired != tc.want {
			t.Errorf("%s: apps wired %v, want %v", name, builds.appsWired, tc.want)
		}
		if builds.apps == nil {
			t.Errorf("%s: the app git routes need their handler, wired or not", name)
		}
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
	cfg.AppBuild.BuildKitImage, cfg.AppEgressExtraDenyCIDRs = "buildkit@sha256:2", []string{"10.0.0.0/8"}
	options := appBuildOptions(cfg)
	if options.Namespace != testAppBuildNamespace || options.RuntimeClass != testRuntimeClass || options.PriorityClass != "builds-low" ||
		options.Slots != 2 || options.PrepareImage != "busybox@sha256:1" || options.BuildKitImage != "buildkit@sha256:2" ||
		options.ToolImage == "" || len(options.ExtraDenyCIDRs) != 1 {
		t.Fatalf("%+v", options)
	}
}

// The spool sits on STORAGE_PATH, and what a crashed publish left there long ago is cleared at boot.
func TestAppBuildSpool_OnStorageAndSweptOfOldLeftovers(t *testing.T) {
	root := t.TempDir()
	dir, err := appBuildSpool(root)
	if err != nil || dir != filepath.Join(root, appBuildSpoolDir) {
		t.Fatalf("spool %s: %v", dir, err)
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
	if _, err := appBuildSpool(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("an old leftover survived the boot")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a publish in flight lost its spool")
	}
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := appBuildSpool(blocked); err == nil {
		t.Error("a STORAGE_PATH that is a file")
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
