package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// The platform-bootstrap Job authenticates as svc-bootstrap with a capability
// token. The /api/admin write gate (EXC-708) must not refuse it before the
// service-account handler, which scopes it to the principals it manages.
func TestCapabilityTokenReachesServiceAccountCreate(t *testing.T) {
	raw := testutil.FixtureToken("bootstrap-capability")
	router, _, _, _ := buildMatrixPlatform(t, config.AppConfig{DeploymentMode: "selfhosted"}, func(deps *handlerDeps, platform *fakePlatform) {
		users := fakestore.NewUsers()
		deps.svcAcctHandler = handler.NewServiceAccountHandler(users, &policyTokenStore{Tokens: platform.Tokens}, nil)
		hash := auth.HashToken(raw)
		platform.ByHash[hash] = &domain.AccessToken{
			TokenHash: hash, UserID: matrixAdminID,
			Permissions: []string{auth.ManageServiceTokensCapability("svc-auth").String()},
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/api/admin/service-accounts/", strings.NewReader(`{"name":"svc-auth"}`))
	req.Header.Set("Authorization", "Bearer "+raw)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("creating svc-auth as the bootstrap token: got %d %s, want 201", w.Code, w.Body.String())
	}
	var created struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Username != "svc-auth" {
		t.Fatalf("response is not the created svc-auth principal: %s (%v)", w.Body.String(), err)
	}
}
