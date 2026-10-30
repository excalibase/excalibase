package endusers

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// EXC-662 step 7: an admin removes one end user's MFA factors.

func TestResetFactorsDeletesTheUsersFactorsWithAUserAdminToken(t *testing.T) {
	client, calls, signer := stubAuth(t, http.StatusNoContent, ``)
	if err := client.ResetFactors(context.Background(), "acme", "proj-a", "admin-1", 7); err != nil {
		t.Fatal(err)
	}
	got := (*calls)[0]
	if got.method != http.MethodDelete || got.path != "/auth/acme/proj-a/users/7/factors" || got.bearer != "Bearer signed-for-admin-1" ||
		got.body != "" || signer.asked[0] != "acme/proj-a/admin-1" {
		t.Fatalf("call = %+v", got)
	}
}

func TestResetFactorsRelaysAuthsRefusal(t *testing.T) {
	client, _, _ := stubAuth(t, http.StatusNotFound, `{"error":"method not enabled","code":"method_not_enabled"}`)
	var refused *RefusedError
	if err := client.ResetFactors(context.Background(), "acme", "proj-a", "admin-1", 7); !errors.As(err, &refused) ||
		refused.Status != http.StatusNotFound || refused.Code != "method_not_enabled" {
		t.Fatalf("err = %v", err)
	}
}
