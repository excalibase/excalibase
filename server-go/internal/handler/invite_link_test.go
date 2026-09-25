package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func TestInviteLinkCarriesTheTokenOnTheRegisterPath(t *testing.T) {
	link, err := url.Parse(inviteLink("a b&c"))
	if err != nil {
		t.Fatal(err)
	}
	if link.Path != "/register" || link.Query().Get("invite") != "a b&c" {
		t.Errorf("link = %s", link)
	}
}

func TestInviteErrorsMapToStatusAndMessage(t *testing.T) {
	cases := []struct {
		err    error
		status int
		msg    string
	}{
		{storage.ErrInviteInvalid, http.StatusBadRequest, storage.ErrInviteInvalid.Error()},
		{errors.Join(storage.ErrAlreadyOrgMember, nil), http.StatusConflict, storage.ErrAlreadyOrgMember.Error()},
		{errors.New("connection reset by peer"), http.StatusInternalServerError, "failed to accept invite"},
	}
	for _, c := range cases {
		if got := inviteErrorStatus(c.err); got != c.status {
			t.Errorf("%v: status %d, want %d", c.err, got, c.status)
		}
		if got := inviteErrorMessage(c.err); got != c.msg {
			t.Errorf("%v: message %q, want %q", c.err, got, c.msg)
		}
	}
}

func TestRegister_InviteSpentBeforeJoiningRemovesTheNewAccount(t *testing.T) {
	for _, failDelete := range []bool{false, true} {
		us := seededStore()
		us.failDelete = failDelete
		org := &inviteOrgStore{tokenHash: hashToken("tok"), acceptErr: storage.ErrInviteInvalid}
		h := registerHandler(us, org, false)

		w := postRegisterWithInvite(h, "late", "late@x.test", "tok")

		if w.Code != http.StatusBadRequest {
			t.Fatalf("failDelete=%v: got %d, want 400", failDelete, w.Code)
		}
		if !failDelete && len(us.users) != 1 {
			t.Errorf("the account created for a spent invite must be removed, have %d users", len(us.users))
		}
	}
}

func TestRegister_TakenUsernameIsAConflict(t *testing.T) {
	h := registerHandler(seededStore(), nil, false)
	if w := postRegister(h, "existing", "other@x.test"); w.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409", w.Code)
	}
}

func acceptRequest(h *OrgHandler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/invites/accept", strings.NewReader(body))
	req = req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "u-accept", Role: "user"}))
	w := httptest.NewRecorder()
	h.AcceptInvite(w, req)
	return w
}

func TestAcceptInvite(t *testing.T) {
	org := &inviteOrgStore{tokenHash: hashToken("tok")}
	h := NewOrgHandler(org, nil)

	if w := acceptRequest(h, `not json`); w.Code != http.StatusBadRequest {
		t.Errorf("malformed body: got %d", w.Code)
	}
	if w := acceptRequest(h, `{"token":""}`); w.Code != http.StatusBadRequest {
		t.Errorf("empty token: got %d", w.Code)
	}
	if w := acceptRequest(h, `{"token":"tok"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"orgId":"org1"`) {
		t.Errorf("valid token: got %d %s", w.Code, w.Body.String())
	}
	if w := acceptRequest(h, `{"token":"tok"}`); w.Code != http.StatusBadRequest {
		t.Errorf("spent token: got %d", w.Code)
	}
	org.acceptErr = storage.ErrAlreadyOrgMember
	if w := acceptRequest(h, `{"token":"tok"}`); w.Code != http.StatusConflict {
		t.Errorf("already a member: got %d", w.Code)
	}
}

func inviteRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/members", nil)
	return req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "u-admin", Role: "user"}))
}

func TestCreateInviteLinkStoresOnlyTheHash(t *testing.T) {
	org := &inviteOrgStore{}
	w := httptest.NewRecorder()
	NewOrgHandler(org, nil).createInviteLink(w, inviteRequest(), "org1", "new@x.test", "viewer")

	if w.Code != http.StatusCreated || len(org.created) != 1 {
		t.Fatalf("got %d, %d invites", w.Code, len(org.created))
	}
	var resp struct {
		InviteLink string `json:"inviteLink"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	link, _ := url.Parse(resp.InviteLink)
	token := link.Query().Get("invite")
	stored := org.created[0]
	if stored.TokenHash != hashToken(token) || stored.TokenHash == token {
		t.Error("the store must receive the token's hash, never the token")
	}
	if stored.ExpiresAt == nil || time.Until(stored.ExpiresAt.Time) < 6*24*time.Hour {
		t.Errorf("expiry = %v, want about 7 days out", stored.ExpiresAt)
	}
}

func TestCreateInviteLinkReportsAStoreFailure(t *testing.T) {
	w := httptest.NewRecorder()
	NewOrgHandler(&inviteOrgStore{createErr: errors.New("down")}, nil).
		createInviteLink(w, inviteRequest(), "org1", "new@x.test", "viewer")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", w.Code)
	}
}
