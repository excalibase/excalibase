package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func tokenOf(t *testing.T, principals []principal, name string) string {
	t.Helper()
	for _, p := range principals {
		if p.Name == name {
			return p.token
		}
	}
	t.Fatalf("no principal %q", name)
	return ""
}

// EXC-761: the capacity endpoint is the operator's. A customer, however
// senior in their own org, must be refused.
func TestCapacityIsRefusedToCustomers(t *testing.T) {
	harness, principals := policyRouter(t)
	for _, name := range []string{"orgViewer", "orgDeveloper", "orgAdmin", "orgOwner", "otherOrgMember"} {
		if code, body := harness.request(http.MethodGet, "/api/capacity", tokenOf(t, principals, name)); code != http.StatusForbidden {
			t.Errorf("%s: got %d %s, want 403", name, code, body)
		}
	}
	if code, _ := harness.request(http.MethodGet, "/api/capacity", ""); code != http.StatusUnauthorized {
		t.Errorf("anonymous: got %d, want 401", code)
	}
	if code, body := harness.request(http.MethodGet, "/api/capacity", tokenOf(t, principals, "platformAdmin")); code != http.StatusOK {
		t.Errorf("platform admin: got %d %s, want 200", code, body)
	}
}

// EXC-761: an S3 key is a credential, so a Developer cannot mint or revoke
// one; an Admin can.
func TestS3KeysAreAdminOnlyToMintOrRevoke(t *testing.T) {
	harness, principals := policyRouter(t)
	keys := "/api/projects/" + matrixProjectA + "/storage/s3-keys"
	for _, name := range []string{"orgViewer", "orgDeveloper"} {
		token := tokenOf(t, principals, name)
		if code, body := harness.request(http.MethodPost, keys, token); code != http.StatusForbidden {
			t.Errorf("%s mint: got %d %s, want 403", name, code, body)
		}
		if code, body := harness.request(http.MethodDelete, keys+"/s3k_1", token); code != http.StatusForbidden {
			t.Errorf("%s revoke: got %d %s, want 403", name, code, body)
		}
	}
	if code, body := harness.request(http.MethodPost, keys, tokenOf(t, principals, "orgAdmin")); code == http.StatusForbidden || code == http.StatusUnauthorized {
		t.Errorf("admin mint: got %d %s, want it to reach the handler", code, body)
	}
}

// EXC-761: any member may make a personal access token for themselves, and it
// can never do more than they can. A token minted by a Developer is refused on
// every credential-minting route, and one minted by a Viewer on every write.
func TestPersonalTokensNeverExceedTheirCreator(t *testing.T) {
	harness, principals := policyRouter(t)
	project := "/api/projects/" + matrixProjectA
	for _, creator := range []string{"orgViewer", "orgDeveloper"} {
		code, body := postJSON(harness, "/api/auth/tokens/", tokenOf(t, principals, creator), `{"name":"mine"}`)
		if code != http.StatusCreated && code != http.StatusOK {
			t.Fatalf("%s must be able to make a token for themselves: %d %s", creator, code, body)
		}
		minted := tokenFromResponse(t, body)
		for _, route := range []struct{ method, path string }{
			{http.MethodPost, project + "/storage/s3-keys"},
			{http.MethodDelete, project + "/storage/s3-keys/s3k_1"},
		} {
			if got, resp := harness.request(route.method, route.path, minted); got != http.StatusForbidden {
				t.Errorf("a token made by %s reached %s %s: %d %s", creator, route.method, route.path, got, resp)
			}
		}
		if creator == "orgViewer" {
			if got, resp := harness.request(http.MethodPut, project+"/cors/", minted); got != http.StatusForbidden {
				t.Errorf("a token made by a Viewer wrote a Developer route: %d %s", got, resp)
			}
		}
	}
}

func tokenFromResponse(t *testing.T, body string) string {
	t.Helper()
	var parsed struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil || parsed.Token == "" {
		t.Fatalf("no token in %q: %v", body, err)
	}
	return parsed.Token
}

func postJSON(h *policyHarness, path, token, body string) (int, string) {
	h.reseed()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}
