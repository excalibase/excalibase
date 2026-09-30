package endusers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stubSigner records what it was asked to sign and hands back a marker token.
type stubSigner struct {
	err   error
	asked []string
}

func (s *stubSigner) SignUserAdmin(projectID, orgSlug, actor string) (string, error) {
	s.asked = append(s.asked, orgSlug+"/"+projectID+"/"+actor)
	if s.err != nil {
		return "", s.err
	}
	return "signed-for-" + actor, nil
}

type seen struct {
	method, path, query, bearer, cookie, body string
}

func stubAuth(t *testing.T, status int, answer string) (*Client, *[]seen, *stubSigner) {
	t.Helper()
	var calls []seen
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, seen{
			method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
			bearer: r.Header.Get("Authorization"), cookie: r.Header.Get("Cookie"), body: string(body),
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	signer := &stubSigner{}
	return NewClient(server.URL+"/", signer, server.Client()), &calls, signer
}

func TestListRelaysAuthsAnswerWithAUserAdminToken(t *testing.T) {
	client, calls, signer := stubAuth(t, http.StatusOK, `{"users":[{"id":3,"email":"a@x.test","role":"user"}],"total":1}`)
	limit, offset := 20, 40
	body, err := client.List(context.Background(), "acme", "proj-a", "admin-1", Page{Limit: &limit, Offset: &offset})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !strings.Contains(string(body), `"email":"a@x.test"`) {
		t.Fatalf("body = %s", body)
	}
	got := (*calls)[0]
	if got.method != http.MethodGet || got.path != "/auth/acme/proj-a/users" || got.query != "limit=20&offset=40" {
		t.Fatalf("call = %+v", got)
	}
	if got.bearer != "Bearer signed-for-admin-1" || signer.asked[0] != "acme/proj-a/admin-1" {
		t.Fatalf("bearer %q, signed %v", got.bearer, signer.asked)
	}
}

func TestListWithoutPagingSendsNoQuery(t *testing.T) {
	client, calls, _ := stubAuth(t, http.StatusOK, `{"users":[]}`)
	if _, err := client.List(context.Background(), "acme", "proj-a", "admin-1", Page{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if (*calls)[0].query != "" {
		t.Fatalf("query = %q", (*calls)[0].query)
	}
}

func TestSetRoleSendsTheChangeAndDecodesTheUser(t *testing.T) {
	client, calls, _ := stubAuth(t, http.StatusOK, `{"id":7,"email":"b@x.test","role":"editor","allowedRoles":["editor","user"]}`)
	user, err := client.SetRole(context.Background(), "acme", "proj-a", "admin-1", 7,
		RoleChange{Role: "editor", AllowedRoles: []string{"editor", "user"}})
	if err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if user.ID != 7 || user.Role != "editor" || len(user.AllowedRoles) != 2 {
		t.Fatalf("user = %+v", user)
	}
	got := (*calls)[0]
	if got.method != http.MethodPut || got.path != "/auth/acme/proj-a/users/7/role" {
		t.Fatalf("call = %+v", got)
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(got.body), &sent)
	if sent["role"] != "editor" || len(sent["allowedRoles"].([]any)) != 2 {
		t.Fatalf("sent %s", got.body)
	}
}

func TestSetRoleWithoutAllowedRolesOmitsThem(t *testing.T) {
	client, calls, _ := stubAuth(t, http.StatusOK, `{"id":7,"role":"editor","allowedRoles":["editor"]}`)
	if _, err := client.SetRole(context.Background(), "acme", "proj-a", "admin-1", 7, RoleChange{Role: "editor"}); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if strings.Contains((*calls)[0].body, "allowedRoles") {
		t.Fatalf("sent %s", (*calls)[0].body)
	}
}

func TestAuthRefusalsKeepTheirStatusAndCode(t *testing.T) {
	client, _, _ := stubAuth(t, http.StatusUnprocessableEntity, `{"error":"allowedRoles must contain role","code":"role_not_allowed","status":422}`)
	_, err := client.SetRole(context.Background(), "acme", "proj-a", "admin-1", 7, RoleChange{Role: "x"})
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Status != 422 || refused.Code != "role_not_allowed" ||
		refused.Message != "allowedRoles must contain role" {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(refused.Error(), "422") {
		t.Fatalf("Error() = %q", refused.Error())
	}
}

func TestARefusalWithoutAMessageStillExplainsItself(t *testing.T) {
	client, _, _ := stubAuth(t, http.StatusNotFound, `not json`)
	_, err := client.List(context.Background(), "acme", "proj-a", "admin-1", Page{})
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Message != "request refused" || refused.Code != "" {
		t.Fatalf("got %v", err)
	}
}

func TestAuthFailuresAreUnavailable(t *testing.T) {
	client, _, _ := stubAuth(t, http.StatusInternalServerError, `{"error":"db down"}`)
	if _, err := client.List(context.Background(), "acme", "proj-a", "admin-1", Page{}); !errors.Is(err, ErrAuthUnavailable) {
		t.Fatalf("500: got %v", err)
	}
	unreachable := NewClient("http://127.0.0.1:1", &stubSigner{}, &http.Client{Timeout: time.Second})
	if _, err := unreachable.List(context.Background(), "acme", "proj-a", "admin-1", Page{}); !errors.Is(err, ErrAuthUnavailable) {
		t.Fatalf("unreachable: got %v", err)
	}
}

func TestAnUnreadableAnswerIsAnError(t *testing.T) {
	client, _, _ := stubAuth(t, http.StatusOK, `not json`)
	if _, err := client.List(context.Background(), "acme", "proj-a", "admin-1", Page{}); err == nil || errors.Is(err, ErrAuthUnavailable) {
		t.Fatalf("list: got %v", err)
	}
	if _, err := client.SetRole(context.Background(), "acme", "proj-a", "admin-1", 7, RoleChange{Role: "x"}); err == nil {
		t.Fatal("set role: decoded junk")
	}
}

func TestUnsafePathSegmentsNeverReachAuth(t *testing.T) {
	client, calls, _ := stubAuth(t, http.StatusOK, `{}`)
	if _, err := client.List(context.Background(), "acme/../x", "proj-a", "admin-1", Page{}); err == nil {
		t.Fatal("an unsafe org slug was accepted")
	}
	if _, err := client.SetRole(context.Background(), "acme", "proj a", "admin-1", 7, RoleChange{Role: "x"}); err == nil {
		t.Fatal("an unsafe project id was accepted")
	}
	if len(*calls) != 0 {
		t.Fatalf("auth was called: %v", *calls)
	}
}

func TestNoTokenMeansNoCall(t *testing.T) {
	client, calls, signer := stubAuth(t, http.StatusOK, `{}`)
	signer.err = errors.New("no key")
	if _, err := client.List(context.Background(), "acme", "proj-a", "admin-1", Page{}); err == nil || errors.Is(err, ErrAuthUnavailable) {
		t.Fatalf("got %v", err)
	}
	if len(*calls) != 0 {
		t.Fatal("auth was called without a token")
	}
}

func TestAMalformedAuthURLIsNotAnOutage(t *testing.T) {
	client := NewClient("http://[bad", &stubSigner{}, http.DefaultClient)
	if _, err := client.List(context.Background(), "acme", "proj-a", "admin-1", Page{}); err == nil || errors.Is(err, ErrAuthUnavailable) {
		t.Fatalf("got %v", err)
	}
}
