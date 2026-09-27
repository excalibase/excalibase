package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

type fakeDomains struct {
	err   error
	calls []string
}

func (f *fakeDomains) view(host string) *service.DomainView {
	return &service.DomainView{Domain: apphost.Domain{ID: "d1", Hostname: host, Status: apphost.DomainPending}, CNAMETarget: "web-p1.apps.example.com"}
}

func (f *fakeDomains) Add(_ context.Context, projectID, appID, host string) (*service.DomainView, error) {
	f.calls = append(f.calls, "add:"+projectID+"/"+appID+"/"+host)
	if f.err != nil {
		return nil, f.err
	}
	return f.view(host), nil
}

func (f *fakeDomains) List(projectID, appID string) ([]*service.DomainView, error) {
	f.calls = append(f.calls, "list:"+projectID+"/"+appID)
	if f.err != nil {
		return nil, f.err
	}
	return []*service.DomainView{f.view("shop.example.com")}, nil
}

func (f *fakeDomains) Verify(_ context.Context, projectID, appID, id string) (*service.DomainView, error) {
	f.calls = append(f.calls, "verify:"+id)
	if f.err != nil {
		return nil, f.err
	}
	return f.view("shop.example.com"), nil
}

func (f *fakeDomains) Remove(_ context.Context, projectID, appID, id string) error {
	f.calls = append(f.calls, "remove:"+id)
	return f.err
}

func domainRequest(domains *fakeDomains, method, path, body string) *httptest.ResponseRecorder {
	h := NewAppDomainHandler(domains)
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/apps/{appId}/domains", h.Routes)
	req := httptest.NewRequest(method, "/api/projects/"+appTestProject+"/apps/app-1/domains"+path, strings.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAppDomainHandler_Surface(t *testing.T) {
	domains := &fakeDomains{}
	if w := domainRequest(domains, http.MethodPost, "/", `{"hostname":"shop.example.com"}`); w.Code != http.StatusCreated ||
		!strings.Contains(w.Body.String(), `"cnameTarget":"web-p1.apps.example.com"`) {
		t.Fatalf("add = %d %s", w.Code, w.Body.String())
	}
	if w := domainRequest(domains, http.MethodGet, "/", ""); w.Code != http.StatusOK {
		t.Fatalf("list = %d", w.Code)
	}
	if w := domainRequest(domains, http.MethodPost, "/d1/verify", ""); w.Code != http.StatusOK {
		t.Fatalf("verify = %d", w.Code)
	}
	if w := domainRequest(domains, http.MethodDelete, "/d1", ""); w.Code != http.StatusNoContent {
		t.Fatalf("remove = %d", w.Code)
	}
	want := []string{"add:" + appTestProject + "/app-1/shop.example.com", "list:" + appTestProject + "/app-1", "verify:d1", "remove:d1"}
	if strings.Join(domains.calls, " ") != strings.Join(want, " ") {
		t.Fatalf("calls = %v", domains.calls)
	}
	var list []map[string]any
	w := domainRequest(domains, http.MethodGet, "/", "")
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || list[0]["hostname"] != "shop.example.com" {
		t.Fatalf("list body = %s", w.Body.String())
	}
}

func TestAppDomainHandler_Refusals(t *testing.T) {
	cases := map[error]int{
		fmt.Errorf("%w: apex", apphost.ErrInvalidDomain):        http.StatusBadRequest,
		apphost.ErrDomainLimit:                                  http.StatusConflict,
		apphost.ErrDomainExists:                                 http.StatusConflict,
		apphost.ErrDomainClaimed:                                http.StatusConflict,
		service.ErrProjectOperationRunning:                      http.StatusConflict,
		fmt.Errorf("%w: no CNAME", service.ErrDomainNotPointed): http.StatusConflict,
		apphost.ErrDomainNotFound:                               http.StatusNotFound,
		apphost.ErrAppNotFound:                                  http.StatusNotFound,
		errors.New("pq: connection refused"):                    http.StatusInternalServerError,
	}
	for err, code := range cases {
		if w := domainRequest(&fakeDomains{err: err}, http.MethodPost, "/d1/verify", ""); w.Code != code {
			t.Errorf("%v: got %d want %d", err, w.Code, code)
		}
	}
	if w := domainRequest(&fakeDomains{}, http.MethodPost, "/", `nope`); w.Code != http.StatusBadRequest {
		t.Errorf("bad json: %d", w.Code)
	}
	if w := domainRequest(&fakeDomains{}, http.MethodPost, "/bad%20id/verify", ""); w.Code != http.StatusBadRequest {
		t.Errorf("bad id: %d", w.Code)
	}
}
