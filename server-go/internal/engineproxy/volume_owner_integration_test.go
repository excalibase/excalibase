//go:build integration

package engineproxy

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
)

// EXC-682 on the real engine: through the proxy a container mounts its own
// app's volume, and neither a sibling app's, another project's, nor one that
// does not exist (which the engine would otherwise create unlabelled).
//
// Run: go test -tags=integration ./internal/engineproxy/ -run TestRealEngineMounts -v

func ownedVolume(t *testing.T, direct *client.Client, name, project, app string) string {
	t.Helper()
	labels := map[string]string{ManagedLabel: "true", "excalibase.project": project, "excalibase.app": app}
	if _, err := direct.VolumeCreate(context.Background(), volume.CreateOptions{Name: name, Driver: "local", Labels: labels}); err != nil {
		t.Fatalf("volume %s: %v", name, err)
	}
	t.Cleanup(func() { _ = direct.VolumeRemove(context.Background(), name, true) })
	return name
}

func appMountBody(project, app, source string) string {
	return `{"Image":"` + itBusybox + `","Cmd":["true"],"Labels":{"excalibase.managed":"true",` +
		`"excalibase.project":"` + project + `","excalibase.app":"` + app + `"},` +
		`"HostConfig":{"Mounts":[{"Type":"volume","Source":"` + source + `","Target":"/data"}]}}`
}

func TestRealEngineMountsOnlyAContainersOwnVolume(t *testing.T) {
	direct, _, proxy := realEngine(t)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	mine := ownedVolume(t, direct, "excalibase-it681-a1-"+suffix, "p1", "a1")
	sibling := ownedVolume(t, direct, "excalibase-it681-a2-"+suffix, "p1", "a2")
	other := ownedVolume(t, direct, "excalibase-it681-b1-"+suffix, "p2", "b1")
	missing := "excalibase-it681-missing-" + suffix
	t.Cleanup(func() { _ = direct.VolumeRemove(context.Background(), missing, true) })

	cases := []struct {
		source string
		want   int
	}{{mine, http.StatusCreated}, {sibling, http.StatusForbidden}, {other, http.StatusForbidden}, {missing, http.StatusForbidden}}
	for i, tc := range cases {
		name := "excalibase-it681-mount-" + strconv.Itoa(i) + "-" + suffix
		remove(t, direct, name)
		if status := postCreate(t, http.DefaultClient, proxy, name, appMountBody("p1", "a1", tc.source)); status != tc.want {
			t.Errorf("mount %s = %d, want %d", tc.source, status, tc.want)
		}
	}
	if _, err := direct.VolumeInspect(context.Background(), missing); err == nil {
		t.Fatal("a refused create left the engine a volume it made")
	}
}
