package email

import (
	"strings"
	"testing"
)

func TestBuildAccountExistsEmail(t *testing.T) {
	existing, err := BuildAccountExistsEmail(AccountExistsData{UserEmail: "a@x.test", SignInURL: "https://studio.test/login"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(existing.TextBody, "already has one") || !strings.Contains(existing.TextBody, "https://studio.test/login") {
		t.Errorf("existing-account text: %s", existing.TextBody)
	}
	taken, err := BuildAccountExistsEmail(AccountExistsData{UserEmail: "a@x.test", UsernameTaken: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(taken.TextBody, "username is already taken") || taken.Subject != "That username is taken" {
		t.Errorf("taken-username message: %q %s", taken.Subject, taken.TextBody)
	}
	if existing.Tags["template"] != "account_exists" {
		t.Errorf("tags = %v", existing.Tags)
	}
}
