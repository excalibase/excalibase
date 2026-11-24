package auth

import "testing"

func TestOnlyPlatformAdminsAndOperatorsSeeRawProvisioningText(t *testing.T) {
	want := map[string]bool{"platform_admin": true, "platform_operator": true, "platform_viewer": false, "user": false, "": false, "other": false}
	for role, expected := range want {
		if got := SeesRawProvisioningText(role); got != expected {
			t.Errorf("role %q: got %v, want %v", role, got, expected)
		}
	}
}
