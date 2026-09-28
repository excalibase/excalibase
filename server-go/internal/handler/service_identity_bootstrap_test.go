package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
)

// svc-bootstrap (EXC-485) replaces the platform admin password in the chart's
// bootstrap Job and token-rotation CronJob. These tests pin what its token
// reaches through the real gate and handlers: the tokens of the principals it
// is granted, each within that principal's configured ceiling — and nothing
// that would turn it back into an admin credential. It holds none of those
// permissions itself.
const (
	svcBootstrapName = "svc-bootstrap"
	svcOtherName     = "svc-other"
	permVaultInit    = "vault:init"
)

func manage(name string) string { return "service-tokens:manage:" + name }

// bootstrapFixture returns the fixture and the raw svc-bootstrap token.
func bootstrapFixture(t *testing.T) (*identityFixture, string) {
	t.Helper()
	f := newIdentityFixture(t)
	raw := f.mintCapability(t, svcBootstrapName, []string{
		manage(svcAuthName), manage(svcGraphqlName), permVaultInit,
	})
	f.auth.SetServiceTokenCeilings(map[string][]string{
		svcAuthName:    {permVaultSigning, permProjectInfo},
		svcGraphqlName: {permPolicies, permProjectInfo},
	})
	return f, raw
}

func (f *identityFixture) mintAs(t *testing.T, bearer, subjectID string, permissions []string) int {
	t.Helper()
	perms, _ := json.Marshal(permissions)
	body := `{"name":"svc","expiresIn":"never","userId":"` + subjectID + `","permissions":` + string(perms) + `}`
	return f.do(http.MethodPost, routeAuthTokens, bearer, body).Code
}

func TestBootstrapTokenCreatesOnlyThePrincipalsItManages(t *testing.T) {
	f, raw := bootstrapFixture(t)
	if w := f.do(http.MethodPost, routeServiceAccounts, raw, `{"name":"`+svcAuthName+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("create svc-auth: "+bodyFmt, w.Code, w.Body.String())
	}
	if w := f.do(http.MethodPost, routeServiceAccounts, raw, `{"name":"`+svcOtherName+`"}`); w.Code != http.StatusForbidden {
		t.Fatalf("create an unmanaged principal: "+bodyFmt, w.Code, w.Body.String())
	}
	if w := f.do(http.MethodDelete, routeServiceAccounts+"/"+svcAuthName, raw, ""); w.Code != http.StatusForbidden {
		t.Fatalf("delete a principal: "+bodyFmt, w.Code, w.Body.String())
	}
}

func TestBootstrapTokenMintsWithinItsOwnPermissions(t *testing.T) {
	f, raw := bootstrapFixture(t)
	svcAuth := f.createServiceAccount(t, svcAuthName)

	w := f.do(http.MethodPost, routeAuthTokens, raw,
		`{"name":"svc-auth","expiresIn":"never","userId":"`+svcAuth.ID+`","permissions":["`+permVaultSigning+`","`+permProjectInfo+`"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("mint svc-auth: "+bodyFmt, w.Code, w.Body.String())
	}
	minted := decodeBody(t, w.Body.String())["token"].(string)
	me := f.do(http.MethodGet, "/api/auth/me", minted, "")
	if me.Code != http.StatusOK || decodeBody(t, me.Body.String())["username"] != svcAuthName {
		t.Fatalf("minted token must authenticate as svc-auth: "+bodyFmt, me.Code, me.Body.String())
	}
}

func TestBootstrapTokenCannotMintAnythingWider(t *testing.T) {
	f, raw := bootstrapFixture(t)
	svcAuth := f.createServiceAccount(t, svcAuthName)
	other := f.createServiceAccount(t, svcOtherName)

	cases := map[string]struct {
		subject     string
		permissions []string
	}{
		"a permission it does not hold":           {svcAuth.ID, []string{"vault:read:projects/*/credentials/admin"}},
		"its own management capability":           {svcAuth.ID, []string{manage(svcGraphqlName)}},
		"vault init":                              {svcAuth.ID, []string{permVaultInit}},
		"a principal it does not manage":          {other.ID, []string{permPolicies}},
		"itself":                                  {"", nil},
		"a human":                                 {humanUserID, []string{permPolicies}},
		"a service token with no permission list": {svcAuth.ID, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if code := f.mintAs(t, raw, tc.subject, tc.permissions); code == http.StatusCreated {
				t.Fatalf("svc-bootstrap minted %v for %q", tc.permissions, tc.subject)
			}
		})
	}
	// A plain PAT for itself would be a platform_admin credential with no
	// permission list: the widest escalation there is.
	if w := f.do(http.MethodPost, routeAuthTokens, raw, `{"name":"escape"}`); w.Code == http.StatusCreated {
		t.Fatalf("svc-bootstrap minted itself a plain PAT: "+bodyFmt, w.Code, w.Body.String())
	}
}

func TestBootstrapTokenListsRotatesAndRevokesOnlyManagedTokens(t *testing.T) {
	f, raw := bootstrapFixture(t)
	svcAuthToken := f.mintCapability(t, svcAuthName, []string{permProjectInfo})
	otherToken := f.mintCapability(t, svcOtherName, []string{permPolicies})
	// A token an admin minted beyond the ceiling is not the bootstrap's to rotate:
	// rotation hands back its secret.
	widened := f.mintCapability(t, svcGraphqlName, []string{"vault:read:projects/*/credentials/admin"})
	if w := f.do(http.MethodPost, routeAuthTokens+"/"+auth.HashToken(widened)+rotateSuffix, raw, ""); w.Code != http.StatusForbidden {
		t.Fatalf("rotate a token beyond the ceiling: "+bodyFmt, w.Code, w.Body.String())
	}

	if w := f.do(http.MethodGet, routeServiceAccounts+"/"+svcAuthName+"/tokens", raw, ""); w.Code != http.StatusOK {
		t.Fatalf("list svc-auth tokens: "+bodyFmt, w.Code, w.Body.String())
	}
	if w := f.do(http.MethodGet, routeServiceAccounts+"/"+svcOtherName+"/tokens", raw, ""); w.Code != http.StatusForbidden {
		t.Fatalf("list svc-other tokens: "+bodyFmt, w.Code, w.Body.String())
	}

	tokenPath := func(secret string) string { return routeAuthTokens + "/" + auth.HashToken(secret) }
	for _, secret := range []string{otherToken, f.admin, f.human} {
		if w := f.do(http.MethodPost, tokenPath(secret)+rotateSuffix, raw, ""); w.Code != http.StatusForbidden {
			t.Errorf("rotate an unmanaged token: "+bodyFmt, w.Code, w.Body.String())
		}
		if w := f.do(http.MethodDelete, tokenPath(secret), raw, ""); w.Code != http.StatusForbidden {
			t.Errorf("revoke an unmanaged token: "+bodyFmt, w.Code, w.Body.String())
		}
	}

	w := f.do(http.MethodPost, tokenPath(svcAuthToken)+rotateSuffix, raw, `{"graceSeconds":60}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("rotate svc-auth: "+bodyFmt, w.Code, w.Body.String())
	}
	rotated := decodeBody(t, w.Body.String())["token"].(string)
	if w := f.do(http.MethodDelete, tokenPath(rotated), raw, ""); w.Code != http.StatusOK {
		t.Fatalf("revoke svc-auth: "+bodyFmt, w.Code, w.Body.String())
	}
}

func TestBootstrapTokenMintsNothingWithoutAConfiguredCeiling(t *testing.T) {
	f, raw := bootstrapFixture(t)
	f.auth.SetServiceTokenCeilings(nil)
	svcAuth := f.createServiceAccount(t, svcAuthName)
	if code := f.mintAs(t, raw, svcAuth.ID, []string{permProjectInfo}); code == http.StatusCreated {
		t.Fatal("minted with no ceiling configured")
	}
}

// An upgrade that narrows a principal's list leaves its stored token outside
// the new ceiling. The bootstrap must still be able to retire it.
func TestBootstrapTokenRevokesATokenBeyondTheCeiling(t *testing.T) {
	f, raw := bootstrapFixture(t)
	drifted := f.mintCapability(t, svcAuthName, []string{permPolicies})
	if w := f.do(http.MethodDelete, routeAuthTokens+"/"+auth.HashToken(drifted), raw, ""); w.Code != http.StatusOK {
		t.Fatalf("revoke a drifted token: "+bodyFmt, w.Code, w.Body.String())
	}
}
