package engineproxy

import "testing"

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
		"auto:uidmapping=0:131073:4096,gidmapping=0:131073:4096",
	} {
		if err := create(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for name, bad := range map[string]string{
		"host":                     "host",
		"keep-id":                  "keep-id",
		"nomap":                    "nomap",
		"another container's":      "container:excalibase-x",
		"a plain auto":             "auto",
		"auto with a size":         "auto:size=65536",
		"the platform user itself": "auto:uidmapping=0:0:65536,gidmapping=0:0:65536",
		"root mapped to base 0":    "auto:uidmapping=0:0:1,gidmapping=0:0:1",
		"users and groups apart":   "auto:uidmapping=0:65537:65536,gidmapping=0:131073:65536",
		"more than one mapping":    "auto:uidmapping=0:65537:65536,gidmapping=0:65537:65536,uidmapping=65536:1:1",
		"a mapping not from root":  "auto:uidmapping=1000:65537:65536,gidmapping=1000:65537:65536",
		"a range beyond a user":    "auto:uidmapping=0:65537:65537,gidmapping=0:65537:65537",
		"an empty range":           "auto:uidmapping=0:65537:0,gidmapping=0:65537:0",
		"a range past 32 bits":     "auto:uidmapping=0:4294967295:65536,gidmapping=0:4294967295:65536",
		"trailing options":         "auto:uidmapping=0:65537:65536,gidmapping=0:65537:65536;size=9",
		"signed numbers":           "auto:uidmapping=0:+65537:65536,gidmapping=0:+65537:65536",
	} {
		if err := create(bad); err == nil {
			t.Errorf("%s: %q was allowed", name, bad)
		}
	}
}
