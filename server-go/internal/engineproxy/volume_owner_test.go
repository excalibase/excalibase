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
		// v1.6.x created MySQL data volumes before volumes named owners, and
		// the engine never adds a label to an existing volume.
		"excalibase-proj-p3-mysql":       {"excalibase.managed": "true"},
		"excalibase-proj-p4-mysql":       {},
		"excalibase-proj-p3-mysql-extra": {"excalibase.managed": "true"},
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
		{"a v1.6 database volume, from its project", map[string]any{"excalibase.project": "p3"}, "excalibase-proj-p3-mysql", true},
		{"a v1.6 database volume, from an app of its project", map[string]any{"excalibase.project": "p3", "excalibase.app": "a3"}, "excalibase-proj-p3-mysql", true},
		{"a v1.6 database volume, from another project", database, "excalibase-proj-p3-mysql", false},
		{"a v1.6 database volume, from an unlabelled container", map[string]any{}, "excalibase-proj-p3-mysql", false},
		{"an unmanaged volume named like a v1.6 database volume", map[string]any{"excalibase.project": "p4"}, "excalibase-proj-p4-mysql", false},
		{"an unowned volume whose name only starts like a database volume", map[string]any{"excalibase.project": "p3"}, "excalibase-proj-p3-mysql-extra", false},
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

// The owner a v1.6 MySQL data volume's name encodes is the one provisioning
// labels a new one with, and nothing else: no other name, no unmanaged
// volume, and never over labels the volume carries.
func TestVolumeOwnersAdoptsOnlyAV16DatabaseVolume(t *testing.T) {
	managed := map[string]string{ManagedLabel: "true"}
	cases := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"excalibase-proj-p3-mysql", managed, "p3"},
		{"excalibase-proj-p-mysql-mysql", managed, "p-mysql"},
		{"excalibase-proj--mysql", managed, ""},
		{"excalibase-proj-mysql", managed, ""},
		{"excalibase-proj-p3-mysql", map[string]string{}, ""},
		{"excalibase-proj-p3-mysql", map[string]string{ManagedLabel: "true", "excalibase.project": "p9"}, "p9"},
		{"excalibase-proj-p3-postgres", managed, ""},
		{"other-p3-mysql", managed, ""},
	}
	for _, tc := range cases {
		owners := VolumeOwners("excalibase-proj-", tc.name, tc.labels)
		if got := owners["excalibase.project"]; got != tc.want {
			t.Errorf("%s %v: project %q, want %q", tc.name, tc.labels, got, tc.want)
		}
		if len(owners) > 1 {
			t.Errorf("%s: owners %v name more than the project", tc.name, owners)
		}
	}
}
