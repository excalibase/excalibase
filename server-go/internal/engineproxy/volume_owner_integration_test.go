//go:build integration

package engineproxy

import (
	"context"
	"net/http"
	"strconv"
	"strings"
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

// v1.6 created a MySQL data volume labelled managed and nothing else, and the
// engine never labels an existing volume: a labelled re-create answers 201
// and the labels stay as they were. Its project still mounts it, by the name
// that encodes it; no other project does.
func TestRealEngineMountsAV16DatabaseVolumeIntoItsProjectOnly(t *testing.T) {
	direct, _, proxy := realEngine(t)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	project := "p" + suffix
	name := "excalibase-it681-" + project + "-mysql"
	legacy := map[string]string{ManagedLabel: "true"}
	if _, err := direct.VolumeCreate(context.Background(), volume.CreateOptions{Name: name, Driver: "local", Labels: legacy}); err != nil {
		t.Fatalf("volume %s: %v", name, err)
	}
	t.Cleanup(func() { _ = direct.VolumeRemove(context.Background(), name, true) })

	relabel := `{"Name":"` + name + `","Driver":"local","Labels":{"excalibase.managed":"true","excalibase.project":"` + project + `"}}`
	if status := postJSON(t, proxy+"/v1.47/volumes/create", relabel); status != http.StatusCreated {
		t.Fatalf("labelled re-create through the proxy = %d", status)
	}
	if inspected, err := direct.VolumeInspect(context.Background(), name); err != nil || inspected.Labels["excalibase.project"] != "" {
		t.Fatalf("the engine labelled an existing volume (%v, %v): this test no longer models v1.6", inspected.Labels, err)
	}

	databaseBody := func(owner string) string {
		return `{"Image":"` + itBusybox + `","Cmd":["true"],"Labels":{"excalibase.managed":"true","excalibase.project":"` + owner + `"},` +
			`"HostConfig":{"Mounts":[{"Type":"volume","Source":"` + name + `","Target":"/var/lib/mysql"}]}}`
	}
	own := "excalibase-it681-" + project + "-own"
	remove(t, direct, own)
	if status := postCreate(t, http.DefaultClient, proxy, own, databaseBody(project)); status != http.StatusCreated {
		t.Fatalf("its project mounting a v1.6 volume = %d, want 201", status)
	}
	other := "excalibase-it681-" + project + "-other"
	remove(t, direct, other)
	if status := postCreate(t, http.DefaultClient, proxy, other, databaseBody("q"+suffix)); status != http.StatusForbidden {
		t.Fatalf("another project mounting a v1.6 volume = %d, want 403", status)
	}
}

func postJSON(t *testing.T, target, body string) int {
	t.Helper()
	resp, err := http.Post(target, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post %s: %v", target, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
