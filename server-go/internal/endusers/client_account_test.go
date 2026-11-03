package endusers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// EXC-662 step 4: one end user's details, their metadata and deleting them.

func TestGetRelaysOneUserAsAuthWroteIt(t *testing.T) {
	client, calls, signer := stubAuth(t, http.StatusOK, `{"id":7,"email":"b@x.test","appMetadata":{"plan":"pro"}}`)
	user, err := client.Get(context.Background(), "acme", "proj-a", "admin-1", 7)
	if err != nil || string(user) != `{"id":7,"email":"b@x.test","appMetadata":{"plan":"pro"}}` {
		t.Fatalf("Get: %s %v", user, err)
	}
	got := (*calls)[0]
	if got.method != http.MethodGet || got.path != "/auth/acme/proj-a/users/7" || got.bearer != "Bearer signed-for-admin-1" || signer.asked[0] != "acme/proj-a/admin-1" {
		t.Fatalf("call = %+v", got)
	}
}

func TestUpdateSendsOnlyTheFieldsGiven(t *testing.T) {
	client, calls, _ := stubAuth(t, http.StatusOK, `{"id":7,"appMetadata":{"plan":"pro"}}`)
	patch := UserPatch{AppMetadata: json.RawMessage(`{"plan":"pro","trial":null}`)}
	user, err := client.Update(context.Background(), "acme", "proj-a", "admin-1", 7, patch)
	if err != nil || string(user) != `{"id":7,"appMetadata":{"plan":"pro"}}` {
		t.Fatalf("Update: %s %v", user, err)
	}
	got := (*calls)[0]
	if got.method != http.MethodPatch || got.path != "/auth/acme/proj-a/users/7" || got.body != `{"appMetadata":{"plan":"pro","trial":null}}` {
		t.Fatalf("call = %+v", got)
	}
	name := ""
	if _, err := client.Update(context.Background(), "acme", "proj-a", "admin-1", 7, UserPatch{FullName: &name}); err != nil {
		t.Fatal(err)
	}
	if (*calls)[1].body != `{"fullName":""}` {
		t.Fatalf("an empty full name was not sent: %s", (*calls)[1].body)
	}
}

func TestDeleteRemovesTheUser(t *testing.T) {
	client, calls, _ := stubAuth(t, http.StatusNoContent, ``)
	if err := client.Delete(context.Background(), "acme", "proj-a", "admin-1", 7); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := (*calls)[0]; got.method != http.MethodDelete || got.path != "/auth/acme/proj-a/users/7" || got.body != "" {
		t.Fatalf("call = %+v", got)
	}
}

func TestAccountCallsKeepAuthsRefusals(t *testing.T) {
	client, _, _ := stubAuth(t, http.StatusConflict, `{"error":"an app table still references the account","code":"user_referenced"}`)
	var refused *RefusedError
	if err := client.Delete(context.Background(), "acme", "proj-a", "admin-1", 7); !errors.As(err, &refused) || refused.Code != "user_referenced" {
		t.Fatalf("delete: %v", err)
	}
	if _, err := client.Get(context.Background(), "acme", "proj-a", "admin-1", 7); !errors.As(err, &refused) || refused.Status != http.StatusConflict {
		t.Fatalf("get: %v", err)
	}
	junk, _, _ := stubAuth(t, http.StatusOK, `not json`)
	if _, err := junk.Get(context.Background(), "acme", "proj-a", "admin-1", 7); err == nil {
		t.Fatal("get: relayed junk")
	}
	if _, err := junk.Update(context.Background(), "acme", "proj-a", "admin-1", 7, UserPatch{}); err == nil {
		t.Fatal("update: relayed junk")
	}
}
