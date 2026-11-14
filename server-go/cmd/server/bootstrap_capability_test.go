package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

// The platform-bootstrap Job authenticates as svc-bootstrap with a capability
// token. The /api/admin write gate (EXC-708) must not refuse it before the
// service-account handler, which scopes it to the principals it manages.
func TestCapabilityTokenReachesServiceAccountCreate(t *testing.T) {
	raw := testutil.FixtureToken("bootstrap-capability")
	router, _, _, _ := buildMatrixPlatform(t, config.AppConfig{DeploymentMode: "selfhosted"}, func(_ *handlerDeps, platform *fakePlatform) {
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

	if strings.Contains(w.Body.String(), "requires a session or an unrestricted token") {
		t.Fatalf("the admin write gate refused the bootstrap capability token: %d %s", w.Code, w.Body.String())
	}
}
