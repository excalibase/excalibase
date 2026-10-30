package apphost

import (
	"strings"
	"testing"
)

// EXC-669: a single host names its own containers excalibase-*; an app may not
// take such a name, so it can never answer for the project's database.
func TestValidateNameReservesThePlatformContainerPrefix(t *testing.T) {
	err := ValidateName("excalibase-p1-postgres")
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("want reserved, got %v", err)
	}
	if err := ValidateName("excalibur"); err != nil {
		t.Fatalf("an ordinary name: %v", err)
	}
}
