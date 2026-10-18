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

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/s3keys"
	"github.com/go-chi/chi/v5"
)

type fakeS3Keys struct {
	created   []s3keys.CreateRequest
	keys      []*s3keys.Key
	createErr error
	listErr   error
}

func (f *fakeS3Keys) Create(_ context.Context, request s3keys.CreateRequest) (string, *s3keys.Key, error) {
	if f.createErr != nil {
		return "", nil, f.createErr
	}
	f.created = append(f.created, request)
	key := &s3keys.Key{ID: "s3k_1", ProjectID: request.ProjectID, Name: request.Name, AccessKeyID: "EXS3AAAAAAAAAAAAAAAA",
		Buckets: request.Buckets, Permissions: request.Permissions, CreatedBy: request.Actor}
	f.keys = append(f.keys, key)
	return "the-secret-shown-once-0000000000000000", key, nil
}

func (f *fakeS3Keys) List(_ context.Context, projectID string) ([]*s3keys.Key, error) {
	return f.keys, f.listErr
}

func (f *fakeS3Keys) Revoke(_ context.Context, projectID, id string) (*s3keys.Key, error) {
	for i, key := range f.keys {
		if key.ID == id && key.ProjectID == projectID {
			f.keys = append(f.keys[:i], f.keys[i+1:]...)
			return key, nil
		}
	}
	return nil, s3keys.ErrNotFound
}

type s3KeysHarness struct {
	keys    *fakeS3Keys
	audit   *recordingAudit
	router  chi.Router
	patExpy *time.Time
}

func newS3KeysHarness() *s3KeysHarness {
	h := &s3KeysHarness{keys: &fakeS3Keys{}, audit: &recordingAudit{}}
	handler := NewS3KeysHandler(h.keys, "https://s3.example.test")
	handler.SetAuditLog(h.audit)
	router := chi.NewRouter()
	router.Route("/api/projects/{projectId}/storage", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := auth.SetUser(r.Context(), &domain.User{ID: "user-dev", Active: true})
				token := &domain.AccessToken{UserID: "user-dev", ExpiresAt: h.patExpy}
				next.ServeHTTP(w, r.WithContext(auth.SetToken(ctx, token)))
			})
		})
		handler.Routes(r)
	})
	h.router = router
	return h
}

func (h *s3KeysHarness) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/projects/proj-1/storage"+path, bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func TestS3Keys_CreateShowsTheSecretOnceWithClientSettings(t *testing.T) {
	h := newS3KeysHarness()
	rec := h.do(http.MethodPost, "/s3-keys", `{"name":"nas","buckets":["photos"],"permissions":{"read":true,"write":true},"expiresInDays":30}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("the secret is never cached")
	}
	var created struct {
		AccessKeyID     string `json:"accessKeyId"`
		SecretAccessKey string `json:"secretAccessKey"`
		Endpoint        string `json:"endpoint"`
		Region          string `json:"region"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.SecretAccessKey == "" || created.AccessKeyID == "" || created.Endpoint != "https://s3.example.test" || created.Region == "" {
		t.Fatalf("created = %s", rec.Body)
	}
	request := h.keys.created[0]
	if request.ProjectID != "proj-1" || request.Actor != "user-dev" || request.Lifetime != 30*24*time.Hour ||
		!request.Read || !request.Write || request.Delete || len(request.Buckets) != 1 {
		t.Fatalf("request = %+v", request)
	}

	listed := h.do(http.MethodGet, "/s3-keys", "")
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), created.SecretAccessKey) ||
		!strings.Contains(listed.Body.String(), created.AccessKeyID) || !strings.Contains(listed.Body.String(), "https://s3.example.test") {
		t.Fatalf("list = %d %s", listed.Code, listed.Body)
	}
	if len(h.audit.entries) != 1 || h.audit.entries[0].Action != "storage_s3_key.create" ||
		strings.Contains(h.audit.entries[0].Details, created.SecretAccessKey) {
		t.Fatalf("audit = %+v", h.audit.entries)
	}
}

func TestS3Keys_ExpiryIsCappedAtTheCallersToken(t *testing.T) {
	h := newS3KeysHarness()
	expiry := time.Now().Add(48 * time.Hour)
	h.patExpy = &expiry
	h.do(http.MethodPost, "/s3-keys", `{"name":"ci","permissions":{"read":true}}`)
	if h.keys.created[0].NotAfter == nil || !h.keys.created[0].NotAfter.Equal(expiry) {
		t.Fatalf("not after = %v", h.keys.created[0].NotAfter)
	}
}

func TestS3Keys_Revoke(t *testing.T) {
	h := newS3KeysHarness()
	h.do(http.MethodPost, "/s3-keys", `{"name":"nas","permissions":{"read":true}}`)
	if rec := h.do(http.MethodDelete, "/s3-keys/s3k_1", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(http.MethodDelete, "/s3-keys/s3k_1", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second revoke = %d %s", rec.Code, rec.Body)
	}
	last := h.audit.entries[len(h.audit.entries)-1]
	if last.Action != "storage_s3_key.revoke" || last.ResourceID != "s3k_1" {
		t.Fatalf("audit = %+v", last)
	}
}

func TestS3Keys_Refusals(t *testing.T) {
	cases := map[string]struct {
		body string
		err  error
		want int
	}{
		"bad json":         {`{`, nil, http.StatusBadRequest},
		"unknown field":    {`{"name":"x","permissions":{"read":true},"admin":true}`, nil, http.StatusBadRequest},
		"negative expiry":  {`{"name":"x","permissions":{"read":true},"expiresInDays":0}`, nil, http.StatusBadRequest},
		"too long expiry":  {`{"name":"x","permissions":{"read":true},"expiresInDays":366}`, nil, http.StatusBadRequest},
		"input error":      {`{"name":"x","permissions":{}}`, &s3keys.InputError{Reason: "needs a permission"}, http.StatusBadRequest},
		"limit reached":    {`{"name":"x","permissions":{"read":true}}`, s3keys.ErrLimitReached, http.StatusConflict},
		"internal failure": {`{"name":"x","permissions":{"read":true}}`, errors.New("vault is sealed at 10.0.0.3"), http.StatusInternalServerError},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newS3KeysHarness()
			h.keys.createErr = tc.err
			rec := h.do(http.MethodPost, "/s3-keys", tc.body)
			if rec.Code != tc.want {
				t.Fatalf("= %d %s, want %d", rec.Code, rec.Body, tc.want)
			}
			if strings.Contains(rec.Body.String(), "10.0.0.3") {
				t.Fatal("internal detail leaked")
			}
		})
	}
	h := newS3KeysHarness()
	if rec := h.do(http.MethodDelete, "/s3-keys/bad%20id", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id = %d", rec.Code)
	}
}
