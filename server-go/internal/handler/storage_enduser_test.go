package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storagesvc"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

const (
	appProject    = "proj_app"
	appBucketPath = "/storage/v1/" + appProject + "/buckets/files"
	appOrigin     = "https://shop.example.com"
	aliceToken    = "alice-token"
	bobToken      = "bob-token"
	staffToken    = "staff-token"
	anonToken     = "anon-token"
)

// fakeEndUserVerifier stands in for the project token check: a known token
// yields its claims, anything else is refused.
type fakeEndUserVerifier struct{ tokens map[string]jwt.MapClaims }

func (v fakeEndUserVerifier) VerifyEndUser(token, projectID string) (jwt.MapClaims, error) {
	claims, ok := v.tokens[token]
	if !ok || claims["projectId"] != projectID {
		return nil, errors.New("invalid jwt")
	}
	return claims, nil
}

// appClaims mirrors the auth service: a signed-in user carries a numeric
// userId; an api-key token (userID 0) carries none.
func appClaims(userID float64, role string, allowed ...string) jwt.MapClaims {
	roles := make([]any, 0, len(allowed)+1)
	roles = append(roles, role)
	for _, r := range allowed {
		roles = append(roles, r)
	}
	claims := jwt.MapClaims{"sub": "someone@example.com", "role": role, "allowed_roles": roles, "projectId": appProject}
	if userID > 0 {
		claims["userId"] = userID
	}
	return claims
}

type endUserFixture struct {
	router  *chi.Mux
	blobs   *fakeObjectStoreForTest
	service *storagesvc.Service
}

func newEndUserFixture(t *testing.T, access storagesvc.BucketAccess) endUserFixture {
	t.Helper()
	blobs := newFakeObjectStoreForTest()
	svc := storagesvc.NewServiceWithObjectStore(newInMemoryBucketStoreForTest(), blobs, nil)
	if _, err := svc.CreateBucket(context.Background(), appProject, storagesvc.CreateBucketRequest{Name: "files", Access: access}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	verifier := fakeEndUserVerifier{tokens: map[string]jwt.MapClaims{
		aliceToken: appClaims(7, "authenticated", "staff_candidate"),
		bobToken:   appClaims(8, "authenticated"),
		staffToken: appClaims(9, "authenticated", "staff"),
		anonToken:  appClaims(0, "anon"),
	}}
	cors := &fakeCorsStore{origins: map[string][]string{appProject: {appOrigin}}}
	h := NewEndUserStorageHandler(NewStorageHandler(svc, nil), verifier, cors)
	r := chi.NewRouter()
	r.Route("/storage/v1/{projectId}", h.Routes)
	return endUserFixture{router: r, blobs: blobs, service: svc}
}

func (f endUserFixture) do(method, path, token string, body any, headers ...string) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

// upload runs the three steps an app runs: mint, PUT (simulated), confirm.
func (f endUserFixture) upload(t *testing.T, token, key string) int {
	t.Helper()
	mint := f.do("POST", appBucketPath+"/upload-url", token, map[string]any{"key": key, "mimeType": "text/plain", "size": 5})
	if mint.Code != http.StatusOK {
		return mint.Code
	}
	var minted storagesvc.UploadURLResponse
	_ = json.Unmarshal(mint.Body.Bytes(), &minted)
	bucket, _ := f.service.Bucket(context.Background(), appProject, "files")
	f.blobs.put(appProject, bucket.ID, ".staging/"+minted.UploadID, 5, "text/plain")
	confirm := f.do("POST", appBucketPath+"/confirm-upload", token, map[string]any{"key": key, "uploadId": minted.UploadID})
	return confirm.Code
}

var ownFolderAccess = storagesvc.BucketAccess{
	"authenticated": {Read: storagesvc.ScopeOwn, Write: storagesvc.ScopeOwn, Delete: storagesvc.ScopeOwn},
}

func TestEndUserStorage_OwnFolderRoundTrip(t *testing.T) {
	f := newEndUserFixture(t, ownFolderAccess)
	if code := f.upload(t, aliceToken, "7/notes.txt"); code != http.StatusCreated {
		t.Fatalf("upload: %d", code)
	}

	list := f.do("GET", appBucketPath+"/objects", aliceToken, nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"key":"7/notes.txt"`) {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	if strings.Contains(list.Body.String(), "ownerId") {
		t.Errorf("app users must not see who uploaded: %s", list.Body.String())
	}

	download := f.do("GET", appBucketPath+"/download-url/7/notes.txt", aliceToken, nil)
	if download.Code != http.StatusOK || !strings.Contains(download.Body.String(), "7/notes.txt") {
		t.Fatalf("download-url: %d %s", download.Code, download.Body.String())
	}

	del := f.do("DELETE", appBucketPath+"/objects/7/notes.txt", aliceToken, nil)
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", del.Code, del.Body.String())
	}
}

func TestEndUserStorage_AnotherUsersFolderIsRefused(t *testing.T) {
	f := newEndUserFixture(t, ownFolderAccess)
	if code := f.upload(t, aliceToken, "7/notes.txt"); code != http.StatusCreated {
		t.Fatalf("setup upload: %d", code)
	}
	cases := []struct{ method, path string }{
		{"GET", appBucketPath + "/download-url/7/notes.txt"},
		{"DELETE", appBucketPath + "/objects/7/notes.txt"},
		{"GET", appBucketPath + "/objects?prefix=7/"},
	}
	for _, tc := range cases {
		if rec := f.do(tc.method, tc.path, bobToken, nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as bob: %d", tc.method, tc.path, rec.Code)
		}
	}
	if code := f.upload(t, bobToken, "7/evil.txt"); code != http.StatusForbidden {
		t.Errorf("bob wrote into alice's folder: %d", code)
	}
	// Bob's own listing does not show alice's file.
	list := f.do("GET", appBucketPath+"/objects", bobToken, nil)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "7/") {
		t.Errorf("bob's listing: %d %s", list.Code, list.Body.String())
	}
}

func TestEndUserStorage_ConfirmIsCheckedLikeTheMint(t *testing.T) {
	f := newEndUserFixture(t, ownFolderAccess)
	rec := f.do("POST", appBucketPath+"/confirm-upload", bobToken, map[string]any{"key": "7/x.txt", "uploadId": "upl_x"})
	if rec.Code != http.StatusForbidden {
		t.Errorf("confirm outside the folder: %d", rec.Code)
	}
}

func TestEndUserStorage_NoRuleAndUnknownBucketLookTheSame(t *testing.T) {
	f := newEndUserFixture(t, nil)
	noRule := f.do("POST", appBucketPath+"/upload-url", aliceToken, map[string]any{"key": "7/a.txt", "mimeType": "text/plain", "size": 5})
	unknown := f.do("POST", "/storage/v1/"+appProject+"/buckets/secret/upload-url", aliceToken, map[string]any{"key": "7/a.txt", "mimeType": "text/plain", "size": 5})
	if noRule.Code != http.StatusForbidden || unknown.Code != http.StatusForbidden {
		t.Fatalf("no rule %d, unknown bucket %d: both must be 403", noRule.Code, unknown.Code)
	}
	if noRule.Body.String() != unknown.Body.String() {
		t.Errorf("bodies differ, which tells a caller which buckets exist: %q vs %q", noRule.Body.String(), unknown.Body.String())
	}
}

func TestEndUserStorage_TokenIsRequiredAndBoundToTheProject(t *testing.T) {
	f := newEndUserFixture(t, ownFolderAccess)
	if rec := f.do("GET", appBucketPath+"/objects", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", rec.Code)
	}
	if rec := f.do("GET", appBucketPath+"/objects", "forged", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad token: %d", rec.Code)
	}
	other := "/storage/v1/proj_other/buckets/files/objects"
	if rec := f.do("GET", other, aliceToken, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("token of another project: %d", rec.Code)
	}
}

func TestEndUserStorage_RoleHeaderPicksAnAllowedRoleOnly(t *testing.T) {
	f := newEndUserFixture(t, storagesvc.BucketAccess{"staff": {Write: storagesvc.ScopeAll}})
	mint := map[string]any{"key": "products/lamp.png", "mimeType": "image/png", "size": 5}
	if rec := f.do("POST", appBucketPath+"/upload-url", staffToken, mint); rec.Code != http.StatusForbidden {
		t.Errorf("default role has no rule: %d", rec.Code)
	}
	if rec := f.do("POST", appBucketPath+"/upload-url", staffToken, mint, "X-Excalibase-Role", "staff"); rec.Code != http.StatusOK {
		t.Errorf("allowed role: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do("POST", appBucketPath+"/upload-url", aliceToken, mint, "X-Excalibase-Role", "staff"); rec.Code != http.StatusForbidden {
		t.Errorf("a role the token does not carry: %d", rec.Code)
	}
}

// Every visitor with the publishable key holds the same api-key token, so an
// own-folder rule gives that token nothing.
func TestEndUserStorage_ApiKeyTokenOwnsNoFolder(t *testing.T) {
	f := newEndUserFixture(t, storagesvc.BucketAccess{"anon": {Read: storagesvc.ScopeOwn, Write: storagesvc.ScopeOwn}})
	if code := f.upload(t, anonToken, "0/a.txt"); code != http.StatusForbidden {
		t.Errorf("api-key upload: %d", code)
	}
	if rec := f.do("GET", appBucketPath+"/objects", anonToken, nil); rec.Code != http.StatusForbidden {
		t.Errorf("api-key listing: %d", rec.Code)
	}
}

// The production verifier is the function gate's: a real ES256 project token.
func TestFunctionHandler_VerifyEndUser(t *testing.T) {
	v, priv := setupVaultWithSigningKey(t)
	h := NewFunctionHandler(nil, nil, nil, nil, nil, "")
	h.SetVault(v)
	h.SetAudienceRequirement(true, "excalibase:")
	token := signES256(t, priv, jwt.MapClaims{
		"projectId": appProject, "userId": float64(7), "role": "authenticated",
		"aud": []string{"excalibase:" + appProject}, "exp": float64(time.Now().Add(time.Hour).Unix()),
	})
	var verifier EndUserVerifier = h
	claims, err := verifier.VerifyEndUser(token, appProject)
	if err != nil || userIDClaim(claims) != "7" {
		t.Fatalf("valid token: %v, %v", claims, err)
	}
	if _, err := verifier.VerifyEndUser(token, "proj_other"); err == nil {
		t.Error("a token of another project was accepted")
	}
}

func TestUserIDClaim(t *testing.T) {
	cases := map[string]jwt.MapClaims{
		"42": {"userId": float64(42)},
		"7":  {"userId": json.Number("7")},
		"":   {"sub": "apikey:3"},
	}
	for want, claims := range cases {
		if got := userIDClaim(claims); got != want {
			t.Errorf("%v: got %q, want %q", claims, got, want)
		}
	}
	for _, bad := range []any{float64(-1), float64(1.5), json.Number("x"), "12"} {
		if got := userIDClaim(jwt.MapClaims{"userId": bad}); got != "" {
			t.Errorf("userId %v gave folder %q", bad, got)
		}
	}
}

func TestEndUserStorage_PublicBucketReadsWithoutARule(t *testing.T) {
	f := newEndUserFixture(t, nil)
	if _, err := f.service.CreateBucket(context.Background(), appProject, storagesvc.CreateBucketRequest{Name: "logos", Public: true}); err != nil {
		t.Fatal(err)
	}
	rec := f.do("GET", "/storage/v1/"+appProject+"/buckets/logos/download-url/brand.png", anonToken, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"public":true`) {
		t.Errorf("public read: %d %s", rec.Code, rec.Body.String())
	}
}

func TestEndUserStorage_ValidationErrorsReachTheCaller(t *testing.T) {
	f := newEndUserFixture(t, ownFolderAccess)
	rec := f.do("POST", appBucketPath+"/upload-url", aliceToken, map[string]any{"key": "7/a.txt", "mimeType": "text/plain"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing size: %d %s", rec.Code, rec.Body.String())
	}
	bad := httptest.NewRequest("POST", appBucketPath+"/upload-url", strings.NewReader("{"))
	bad.Header.Set("Authorization", "Bearer "+aliceToken)
	out := httptest.NewRecorder()
	f.router.ServeHTTP(out, bad)
	if out.Code != http.StatusBadRequest {
		t.Errorf("bad json: %d", out.Code)
	}
}

func TestEndUserStorage_CORSFollowsTheProjectAllowlist(t *testing.T) {
	f := newEndUserFixture(t, ownFolderAccess)
	pre := httptest.NewRequest("OPTIONS", appBucketPath+"/upload-url", nil)
	pre.Header.Set("Origin", appOrigin)
	pre.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, pre)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != appOrigin {
		t.Fatalf("listed origin preflight: %d %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "DELETE") {
		t.Errorf("methods: %q", rec.Header().Get("Access-Control-Allow-Methods"))
	}

	other := httptest.NewRequest("OPTIONS", appBucketPath+"/upload-url", nil)
	other.Header.Set("Origin", "https://other-project.example.com")
	rec = httptest.NewRecorder()
	f.router.ServeHTTP(rec, other)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unlisted origin granted %q", got)
	}
	// The engine's, auth's and functions' rule (EXC-563): an unlisted
	// preflight is a 403, and every answer to an Origin varies by it.
	if rec.Code != http.StatusForbidden {
		t.Errorf("unlisted origin preflight: %d, want 403", rec.Code)
	}
	if rec.Header().Get("Vary") != "Origin" {
		t.Errorf("unlisted origin Vary: %q", rec.Header().Get("Vary"))
	}

	noOrigin := httptest.NewRequest("OPTIONS", appBucketPath+"/upload-url", nil)
	rec = httptest.NewRecorder()
	f.router.ServeHTTP(rec, noOrigin)
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight without Origin: %d, want 204", rec.Code)
	}
}
