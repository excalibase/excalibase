package apphost

import (
	"strings"
	"testing"
)

// EXC-610: the plan's limit counts every app from creation, deployed or not; the refusal says which hold it.
func TestAppLimitErrorNamesTheAppsHoldingTheSlots(t *testing.T) {
	err := AppLimitError{Limit: 2, Holders: []SlotHolder{{Name: "live-sports", Deployed: false}, {Name: "web", Deployed: true}}}
	message := err.Error()
	for _, want := range []string{"2 apps", "live-sports (never deployed), web", "deployed or not", "delete one"} {
		if !strings.Contains(message, want) {
			t.Errorf("message lacks %q: %s", want, message)
		}
	}
	if bare := (AppLimitError{Limit: 1}).Error(); !strings.Contains(bare, "1 apps") && !strings.Contains(bare, "1 app") {
		t.Errorf("without holders: %s", bare)
	}
}
