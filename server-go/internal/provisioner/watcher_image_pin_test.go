package provisioner

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const pinnedWatcher = "excalibase/excalibase-watcher-go:1.0.0"

func watcherSpecForTest() WatcherSpec {
	return WatcherSpec{Namespace: "org1-proj", ProjectID: "proj", DBName: "app",
		Username: "cdc_watcher", Password: "secret", NatsUser: "proj-watcher"}
}

// EXC-346: every tenant's watcher ran :latest, which now means "newest stable
// release" — so tenants would follow releases instead of the version their
// platform shipped with. The install pins the released version.
func TestDeployWatcherRunsThePinnedImage(t *testing.T) {
	mock := k8s.NewMockClient()
	prov := NewPostgreSQLProvisioner(mock, "/charts/excalibase-watcher-go")
	prov.SetWatcherImage(pinnedWatcher)

	if err := prov.DeployWatcher(context.Background(), watcherSpecForTest()); err != nil {
		t.Fatalf("DeployWatcher: %v", err)
	}

	image := mock.HelmReleases["org1-proj/"+watcherReleaseName]["image"].(map[string]interface{})
	rendered := image["repository"].(string) + ":" + image["tag"].(string)
	if rendered != pinnedWatcher {
		t.Errorf("chart renders %q, want %q", rendered, pinnedWatcher)
	}
}

// No configured image is not a reason to guess one: a watcher running
// whatever :latest means today is exactly what pinning removes.
func TestDeployWatcherRefusesWithoutAConfiguredImage(t *testing.T) {
	mock := k8s.NewMockClient()
	prov := NewPostgreSQLProvisioner(mock, "/charts/excalibase-watcher-go")

	err := prov.DeployWatcher(context.Background(), watcherSpecForTest())
	if err == nil || !strings.Contains(err.Error(), "WATCHER_IMAGE") {
		t.Fatalf("want an error naming WATCHER_IMAGE, got %v", err)
	}
	if len(mock.HelmReleases) != 0 {
		t.Errorf("installed %v with no image configured", mock.HelmReleases)
	}
}

func TestSplitImageReference(t *testing.T) {
	cases := []struct{ ref, repository, tag string }{
		{pinnedWatcher, "excalibase/excalibase-watcher-go", "1.0.0"},
		{"excalibase/excalibase-watcher-go:1.0.0@sha256:6c07c9d93ef7", "excalibase/excalibase-watcher-go",
			"1.0.0@sha256:6c07c9d93ef7"},
		{"excalibase/excalibase-watcher-go:main-352f9ba", "excalibase/excalibase-watcher-go", "main-352f9ba"},
		{"localhost:5000/watcher:1.0.0", "localhost:5000/watcher", "1.0.0"},
	}
	for _, testCase := range cases {
		repository, tag, err := splitImageReference(testCase.ref)
		if err != nil || repository != testCase.repository || tag != testCase.tag {
			t.Errorf("%q: got (%q, %q, %v), want (%q, %q)", testCase.ref,
				repository, tag, err, testCase.repository, testCase.tag)
		}
	}
}

// The chart renders repository:tag, so a reference with no tag cannot be
// expressed — and a digest alone is what CNPG refused in EXC-429.
func TestSplitImageReferenceRefusesAMissingTag(t *testing.T) {
	for _, ref := range []string{"excalibase/excalibase-watcher-go", "excalibase/excalibase-watcher-go@sha256:abc"} {
		if _, _, err := splitImageReference(ref); err == nil {
			t.Errorf("%q: accepted a reference with no tag", ref)
		}
	}
}
