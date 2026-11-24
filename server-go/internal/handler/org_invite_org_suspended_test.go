package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/orgguard"
)

// The org of a suspended owner must not keep growing: its admins mint
// invites and invitees (and pre-suspension invites the owner sent) join it.
func suspendedOrgGuard(orgs ...string) *orgguard.Guard {
	set := map[string]bool{}
	for _, id := range orgs {
		set[id] = true
	}
	return orgguard.New(nil, func(context.Context) (map[string]bool, error) { return set, nil })
}

func guardedOrgHandler(t *testing.T, org *inviteOrgStore) *OrgHandler {
	t.Helper()
	h := NewOrgHandler(org, nil)
	guarded, ok := any(h).(interface{ SetOrgGuard(*orgguard.Guard) })
	if !ok {
		t.Fatal("OrgHandler has no org guard: a suspended owner's org still invites and admits members")
	}
	guarded.SetOrgGuard(suspendedOrgGuard("org1"))
	return h
}

func TestInviteOrgMember_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	org := &inviteOrgStore{}
	h := guardedOrgHandler(t, org)
	w := httptest.NewRecorder()
	h.createInviteLink(w, inviteRequest(), "org1", "new@x.test", "admin")
	if w.Code != http.StatusForbidden || len(org.created) != 0 {
		t.Fatalf("got %d with %d invites, want 403 and none", w.Code, len(org.created))
	}
}

func TestAcceptInvite_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	org := &inviteOrgStore{tokenHash: hashToken("tok")}
	h := guardedOrgHandler(t, org)
	if w := acceptRequest(h, `{"token":"tok"}`); w.Code != http.StatusForbidden || len(org.accepted) != 0 {
		t.Fatalf("got %d, %d accepted; want 403 and the invite unspent", w.Code, len(org.accepted))
	}
}
