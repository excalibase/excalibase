package engineproxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// EXC-682: a container mounts only a volume that exists and belongs to it: the
// volume's project, app and build labels must be the container's own.

func volumeEngine(t *testing.T) (*fakeEngine, string) {
	t.Helper()
	engine, proxy := appsProxyEngine(t)
	engine.volumes = map[string]map[string]string{
		"excalibase-proj-disk-a1":  {"excalibase.managed": "true", "excalibase.project": "p1", "excalibase.app": "a1"},
		"excalibase-proj-disk-a2":  {"excalibase.managed": "true", "excalibase.project": "p1", "excalibase.app": "a2"},
		"excalibase-proj-disk-b1":  {"excalibase.managed": "true", "excalibase.project": "p2", "excalibase.app": "b1"},
		"excalibase-proj-p1-mysql": {"excalibase.managed": "true", "excalibase.project": "p1"},
		"excalibase-proj-unowned":  {"excalibase.managed": "true"},
		"excalibase-proj-foreign":  {"excalibase.project": "p1", "excalibase.app": "a1"},
	}
	return engine, proxy
}

// mountingCreate is a container labelled for project/app that mounts the volume, as a mount or a bind.
func mountingCreate(t *testing.T, labels map[string]any, volume string, asBind bool) string {
	t.Helper()
	return string(createBody(t, func(b map[string]any) {
		all := map[string]any{"excalibase.managed": "true"}
		for key, value := range labels {
			all[key] = value
		}
		b["Labels"] = all
		if asBind {
			hostConfig(b)["Binds"] = []any{volume + ":/data"}
			return
		}
		hostConfig(b)["Mounts"] = []any{map[string]any{"Type": "volume", "Source": volume, "Target": "/data"}}
	}))
}

func TestAContainerMountsOnlyItsOwnVolume(t *testing.T) {
	app := map[string]any{"excalibase.project": "p1", "excalibase.app": "a1"}
	database := map[string]any{"excalibase.project": "p1"}
	cases := []struct {
		name    string
		labels  map[string]any
		volume  string
		allowed bool
	}{
		{"its own disk", app, "excalibase-proj-disk-a1", true},
		{"its project's database volume from an app", app, "excalibase-proj-p1-mysql", true},
		{"its own database volume", database, "excalibase-proj-p1-mysql", true},
		{"a sibling app's disk", app, "excalibase-proj-disk-a2", false},
		{"another project's disk", app, "excalibase-proj-disk-b1", false},
		{"an app's disk from a database container", database, "excalibase-proj-disk-a1", false},
		{"an unlabelled container", map[string]any{}, "excalibase-proj-disk-a1", false},
		{"a volume with no owner", app, "excalibase-proj-unowned", false},
		{"a volume the platform does not manage", app, "excalibase-proj-foreign", false},
		{"a volume that does not exist", app, "excalibase-proj-missing", false},
	}
	for _, tc := range cases {
		for _, asBind := range []bool{false, true} {
			engine, proxy := volumeEngine(t)
			resp := call(t, proxy, http.MethodPost, "/v1.47/containers/create?name=excalibase-app-x", mountingCreate(t, tc.labels, tc.volume, asBind))
			forwarded := engine.reached("POST /v1.47/containers/create")
			if forwarded != tc.allowed {
				t.Errorf("%s (bind %v): forwarded %v, want %v (status %d)", tc.name, asBind, forwarded, tc.allowed, resp.StatusCode)
			}
			if !tc.allowed && resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s (bind %v): status %d, want 403", tc.name, asBind, resp.StatusCode)
			}
		}
	}
}

// The project's database container sits beside the app's own label; an app
// that claims a different app's label is refused even on its own project.
func TestAVolumeOwnerLabelIsMatchedExactly(t *testing.T) {
	engine, proxy := volumeEngine(t)
	body := mountingCreate(t, map[string]any{"excalibase.project": "p1", "excalibase.app": "a1x"}, "excalibase-proj-disk-a1", false)
	call(t, proxy, http.MethodPost, "/v1.47/containers/create?name=excalibase-app-x", body)
	if engine.reached("POST /v1.47/containers/create") {
		t.Fatal("a near-miss app label mounted the disk")
	}
}

func TestAnonymousVolumesNeedNoOwner(t *testing.T) {
	engine, proxy := volumeEngine(t)
	body := string(createBody(t, func(b map[string]any) {
		hostConfig(b)["Mounts"] = []any{map[string]any{"Type": "volume", "Target": "/var/lib/postgresql/data"}}
	}))
	call(t, proxy, http.MethodPost, "/v1.47/containers/create?name=excalibase-p9-postgres", body)
	if !engine.reached("POST /v1.47/containers/create") {
		t.Fatal("an anonymous data volume was refused")
	}
}

func TestCheckVolumeCreateNeedsAnOwner(t *testing.T) {
	policy := appsPolicy()
	for _, owner := range []string{"excalibase.project", "excalibase.app", "excalibase.build"} {
		body := map[string]any{"Name": "excalibase-proj-d", "Labels": map[string]any{"excalibase.managed": "true", owner: "x1"}}
		if err := policy.CheckVolumeCreate(mustJSON(t, body)); err != nil {
			t.Errorf("owner %s refused: %v", owner, err)
		}
	}
	unowned := map[string]any{"Name": "excalibase-proj-d", "Labels": map[string]any{"excalibase.managed": "true", "excalibase.project": ""}}
	if err := policy.CheckVolumeCreate(mustJSON(t, unowned)); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("a volume with no owner was created: %v", err)
	}
}

func TestVolumeOwnersReadsBothForms(t *testing.T) {
	body := createBody(t, func(b map[string]any) {
		hostConfig(b)["Binds"] = []any{"excalibase-proj-a:/a:ro"}
		hostConfig(b)["Mounts"] = []any{
			map[string]any{"Type": "volume", "Source": "excalibase-proj-b", "Target": "/b"},
			map[string]any{"Type": "volume", "Target": "/anon"},
			map[string]any{"Type": "tmpfs", "Target": "/tmp"},
		}
	})
	var create createVolumes
	if err := json.Unmarshal(body, &create); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(create.named(), ","); got != "excalibase-proj-a,excalibase-proj-b" {
		t.Fatalf("named volumes = %s", got)
	}
}
