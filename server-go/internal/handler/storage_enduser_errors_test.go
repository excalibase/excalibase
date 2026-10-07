package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

// failingBucketLookup is a catalogue whose bucket reads fail.
type failingBucketLookup struct{ *inMemoryBucketStoreForTest }

func (failingBucketLookup) GetBucket(context.Context, string, string) (*storagesvc.Bucket, error) {
	return nil, errors.New("catalogue down")
}

// failingTierStore is a project store that cannot be read.
type failingTierStore struct{ *inMemoryInstanceStore }

func (failingTierStore) FindByProjectID(string) (*domain.DatabaseInstance, error) {
	return nil, errors.New("project store down")
}

// codedVerifier refuses every token with a coded rejection.
type codedVerifier struct{}

func (codedVerifier) VerifyEndUser(string, string) (jwt.MapClaims, error) {
	return nil, &codedJWTError{code: "token_audience_mismatch", reason: "wrong audience"}
}

func routerFor(h *EndUserStorageHandler) *chi.Mux {
	r := chi.NewRouter()
	r.Route("/storage/v1/{projectId}", h.Routes)
	return r
}

func TestEndUserStorage_CodedTokenRejectionAnswersItsCode(t *testing.T) {
	f := newEndUserFixture(t, ownFolderAccess)
	f.router = routerFor(NewEndUserStorageHandler(NewStorageHandler(f.service, nil), codedVerifier{}, nil))
	rec := f.do("GET", appBucketPath+"/objects", aliceToken, nil)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "token_audience_mismatch") {
		t.Fatalf("coded rejection: %d %s", rec.Code, rec.Body.String())
	}
}

// A catalogue failure is a 500 that says nothing about the store, on every
// route, and never a refusal that would look like a rule.
func TestEndUserStorage_CatalogueFailureIsAServerError(t *testing.T) {
	f := newEndUserFixture(t, ownFolderAccess)
	svc := storagesvc.NewServiceWithObjectStore(failingBucketLookup{newInMemoryBucketStoreForTest()}, f.blobs, nil)
	f.router = routerFor(NewEndUserStorageHandler(NewStorageHandler(svc, nil), endUserVerifierForTest(), nil))
	for _, call := range []struct{ method, path string }{
		{"GET", appBucketPath + "/objects"},
		{"GET", appBucketPath + "/download-url/7/a.txt"},
		{"DELETE", appBucketPath + "/objects/7/a.txt"},
	} {
		rec := f.do(call.method, call.path, aliceToken, nil)
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "catalogue down") {
			t.Errorf("%s %s: %d %s", call.method, call.path, rec.Code, rec.Body.String())
		}
	}
}

func TestEndUserStorage_TierFailureIsAServerError(t *testing.T) {
	f := newEndUserFixture(t, ownFolderAccess)
	tiers := failingTierStore{&inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}}}
	f.router = routerFor(NewEndUserStorageHandler(NewStorageHandler(f.service, tiers), endUserVerifierForTest(), nil))
	mint := f.do("POST", appBucketPath+"/upload-url", aliceToken, map[string]any{"key": "7/a.txt", "mimeType": "text/plain", "size": 5})
	confirm := f.do("POST", appBucketPath+"/confirm-upload", aliceToken, map[string]any{"key": "7/a.txt", "uploadId": "upl_x"})
	for name, rec := range map[string]int{"upload-url": mint.Code, "confirm-upload": confirm.Code} {
		if rec != http.StatusInternalServerError {
			t.Errorf("%s: %d, want 500", name, rec)
		}
	}
}

// Requests the rule allows but the store refuses answer the store's reason.
func TestEndUserStorage_InvalidRequestsInsideTheFolderAre400(t *testing.T) {
	f := newEndUserFixture(t, ownFolderAccess)
	cases := []struct {
		name   string
		method string
		path   string
		body   any
		want   int
	}{
		{"upload with an empty path segment", "POST", appBucketPath + "/upload-url", map[string]any{"key": "7/a//b", "mimeType": "text/plain", "size": 5}, http.StatusBadRequest},
		{"confirm an upload never staged", "POST", appBucketPath + "/confirm-upload", map[string]any{"key": "7/a.txt", "uploadId": "upl_00000000000000000000000000000000"}, 0},
		{"download with an empty path segment", "GET", appBucketPath + "/download-url/7/a//b", nil, http.StatusBadRequest},
		{"delete a file that is not there", "DELETE", appBucketPath + "/objects/7/a//b", nil, http.StatusBadRequest},
		{"list with a limit that is not a number", "GET", appBucketPath + "/objects?limit=ten", nil, http.StatusBadRequest},
		{"list a malformed prefix inside the folder", "GET", appBucketPath + "/objects?prefix=7/a//", nil, http.StatusBadRequest},
		{"list another user's folder", "GET", appBucketPath + "/objects?prefix=8/", nil, http.StatusForbidden},
	}
	for _, tc := range cases {
		rec := f.do(tc.method, tc.path, aliceToken, tc.body)
		if tc.want == 0 {
			if rec.Code < 400 || rec.Code >= 500 {
				t.Errorf("%s: %d %s, want a 4xx", tc.name, rec.Code, rec.Body.String())
			}
			continue
		}
		if rec.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, rec.Code, rec.Body.String(), tc.want)
		}
	}
}
