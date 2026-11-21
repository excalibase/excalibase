package engineproxy

import (
	"net/http"
	"testing"
)

// EXC-682: an app may run in one explicit range of the platform user's
// namespace (root of the container mapped to the range, users and groups the
// same), never another user namespace mode.
func TestCheckCreateAllowsAnExplicitUserRangeOnly(t *testing.T) {
	create := func(mode string) error {
		body := createBody(t, func(b map[string]any) {
			hostConfig(b)["UsernsMode"] = mode
			hostConfig(b)["NetworkMode"] = "excalibase-proj-p1"
			b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{"excalibase-proj-p1": map[string]any{}}}
		})
		return testPolicy().CheckCreate("excalibase-p1-app-web", body)
	}
	for _, ok := range []string{
		"auto:uidmapping=0:65537:65536,gidmapping=0:65537:65536",
		"auto:uidmapping=0:131073:65536,gidmapping=0:131073:65536",
		"auto:uidmapping=0:6619137:65536,gidmapping=0:6619137:65536",
	} {
		if err := create(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for name, bad := range map[string]string{
		"host":                      "host",
		"keep-id":                   "keep-id",
		"nomap":                     "nomap",
		"another container's":       "container:excalibase-x",
		"a plain auto":              "auto",
		"auto with a size":          "auto:size=65536",
		"the platform user itself":  "auto:uidmapping=0:0:65536,gidmapping=0:0:65536",
		"root mapped to base 0":     "auto:uidmapping=0:0:1,gidmapping=0:0:1",
		"users and groups apart":    "auto:uidmapping=0:65537:65536,gidmapping=0:131073:65536",
		"more than one mapping":     "auto:uidmapping=0:65537:65536,gidmapping=0:65537:65536,uidmapping=65536:1:1",
		"a mapping not from root":   "auto:uidmapping=1000:65537:65536,gidmapping=1000:65537:65536",
		"a range beyond a user":     "auto:uidmapping=0:65537:65537,gidmapping=0:65537:65537",
		"an empty range":            "auto:uidmapping=0:65537:0,gidmapping=0:65537:0",
		"a range past 32 bits":      "auto:uidmapping=0:4294967295:65536,gidmapping=0:4294967295:65536",
		"trailing options":          "auto:uidmapping=0:65537:65536,gidmapping=0:65537:65536;size=9",
		"a smaller range":           "auto:uidmapping=0:65537:4096,gidmapping=0:65537:4096",
		"a range in the shared ids": "auto:uidmapping=0:1:65536,gidmapping=0:1:65536",
		"a range off the grid":      "auto:uidmapping=0:65538:65536,gidmapping=0:65538:65536",
		"a range overlapping two":   "auto:uidmapping=0:98305:65536,gidmapping=0:98305:65536",
		"signed numbers":            "auto:uidmapping=0:+65537:65536,gidmapping=0:+65537:65536",
	} {
		if err := create(bad); err == nil {
			t.Errorf("%s: %q was allowed", name, bad)
		}
	}
}

// A volume labelled with its app's range is mounted only by a container in exactly that range;
// one without a range, only by a container in none: an app cannot be put in another's range.
func TestAContainerMountsARangedVolumeOnlyInItsOwnRange(t *testing.T) {
	const mine = "auto:uidmapping=0:65537:65536,gidmapping=0:65537:65536"
	const other = "auto:uidmapping=0:131073:65536,gidmapping=0:131073:65536"
	app := map[string]any{"excalibase.project": "p1", "excalibase.app": "a1"}
	cases := []struct {
		name    string
		volume  string
		mode    string
		allowed bool
	}{
		{"its own range", "excalibase-proj-ranged", mine, true},
		{"no range for a ranged volume", "excalibase-proj-ranged", "", false},
		{"another app's range", "excalibase-proj-ranged", other, false},
		{"a range for a volume with none", "excalibase-proj-disk-a1", mine, false},
		{"no range for a volume with none", "excalibase-proj-disk-a1", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine, proxy := volumeEngine(t)
			engine.volumes["excalibase-proj-ranged"] = map[string]string{"excalibase.managed": "true",
				"excalibase.project": "p1", "excalibase.app": "a1", "excalibase.userns": "65537:65536"}
			body := createBody(t, func(b map[string]any) {
				b["Labels"] = map[string]any{"excalibase.managed": "true", "excalibase.project": app["excalibase.project"], "excalibase.app": app["excalibase.app"]}
				hostConfig(b)["UsernsMode"] = tc.mode
				hostConfig(b)["Mounts"] = []any{map[string]any{"Type": "volume", "Source": tc.volume, "Target": "/data"}}
			})
			call(t, proxy, http.MethodPost, "/v1.47/containers/create?name=excalibase-app-x", string(body))
			if forwarded := engine.reached("POST /v1.47/containers/create"); forwarded != tc.allowed {
				t.Fatalf("forwarded = %v, want %v", forwarded, tc.allowed)
			}
		})
	}
}
