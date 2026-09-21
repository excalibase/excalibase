package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

type recordingPublisher struct{ events []domain.PolicyChangeEvent }

func (r *recordingPublisher) PublishPolicyChange(_ context.Context, evt domain.PolicyChangeEvent) {
	r.events = append(r.events, evt)
}

// EXC-437: the engine caches a project's schema for 30 minutes and evicts it on
// policies.<projectId>.changed. Policy and grant writes publish that; DDL never
// did, so a table created through the API stayed invisible for half an hour.
func TestDDLAnnouncesASchemaChange(t *testing.T) {
	publisher := &recordingPublisher{}
	handler := &SchemaHandler{connCache: map[string]*connEntry{}}
	handler.SetPublisher(publisher)

	router := chi.NewRouter()
	router.Route("/api/schema/{projectId}", func(r chi.Router) {
		r.Use(handler.AnnounceSchemaChange)
		r.Post("/ddl", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})

	request := httptest.NewRequest(http.MethodPost, "/api/schema/proj-1/ddl", nil)
	router.ServeHTTP(httptest.NewRecorder(), request)

	if len(publisher.events) != 1 {
		t.Fatalf("events: got %d, want 1", len(publisher.events))
	}
	if publisher.events[0].ProjectID != "proj-1" {
		t.Errorf("projectId: got %q, want proj-1", publisher.events[0].ProjectID)
	}
	if publisher.events[0].Kind != domain.SchemaChangeKind {
		t.Errorf("kind: got %q, want %q", publisher.events[0].Kind, domain.SchemaChangeKind)
	}
}

// A read changes nothing, and a refused write changes nothing either. Either
// one announcing would evict every engine's schema on ordinary traffic.
func TestOnlySuccessfulWritesAnnounce(t *testing.T) {
	cases := []struct {
		name   string
		method string
		status int
	}{
		{"a read", http.MethodGet, http.StatusOK},
		{"a rejected write", http.MethodPost, http.StatusBadRequest},
		{"a failed write", http.MethodPost, http.StatusInternalServerError},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			publisher := &recordingPublisher{}
			handler := &SchemaHandler{connCache: map[string]*connEntry{}}
			handler.SetPublisher(publisher)

			router := chi.NewRouter()
			router.Route("/api/schema/{projectId}", func(r chi.Router) {
				r.Use(handler.AnnounceSchemaChange)
				r.HandleFunc("/ddl", func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(testCase.status)
				})
			})

			request := httptest.NewRequest(testCase.method, "/api/schema/proj-1/ddl", nil)
			router.ServeHTTP(httptest.NewRecorder(), request)

			if len(publisher.events) != 0 {
				t.Errorf("events: got %d, want none", len(publisher.events))
			}
		})
	}
}

// Nothing wired means nothing to announce, not a panic.
func TestAnnounceWithoutAPublisherIsQuiet(t *testing.T) {
	handler := &SchemaHandler{connCache: map[string]*connEntry{}}

	router := chi.NewRouter()
	router.Route("/api/schema/{projectId}", func(r chi.Router) {
		r.Use(handler.AnnounceSchemaChange)
		r.Post("/ddl", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/schema/proj-1/ddl", nil))

	if recorder.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", recorder.Code)
	}
}
