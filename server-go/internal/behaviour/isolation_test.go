package behaviour

import (
	"os/exec"
	"strings"
	"testing"
)

// The detector can read and record; it must not be able to reach anything that
// suspends, blocks or limits an app, project or account, through any import.
func TestBehaviourNeverReachesSuspensionOrEnforcement(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	forbidden := []string{
		"/internal/adminconsole", "/internal/apphost", "/internal/middleware", "/internal/orgguard",
		"/internal/sites", "/internal/k8s", "/internal/abuse", "/internal/config",
	}
	for _, pkg := range strings.Fields(string(out)) {
		for _, banned := range forbidden {
			if strings.HasSuffix(pkg, banned) || strings.Contains(pkg, banned+"/") {
				t.Errorf("behaviour imports %s, which can act on an app or account", pkg)
			}
		}
	}
}
