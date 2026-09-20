package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

// EXC-409: the endpoint response has a place for a Mongo port, and for a
// DocumentDB project it is filled in along with the strings a Mongo client
// needs. For every other project it stays absent — a client must be able to
// tell "no Mongo endpoint" from "one I was not told the port of".

// stubDBEndpoints serves one prepared view.
type stubDBEndpoints struct{ view service.DBEndpointView }

func (s stubDBEndpoints) Describe(context.Context, string) (service.DBEndpointView, error) {
	return s.view, nil
}
func (s stubDBEndpoints) SetPublic(context.Context, string, bool) (service.DBEndpointView, error) {
	return s.view, nil
}
func (s stubDBEndpoints) SetRequireTLS(context.Context, string, bool) (service.DBEndpointView, error) {
	return s.view, nil
}

func endpointBody(t *testing.T, view service.DBEndpointView) map[string]any {
	t.Helper()
	h := &ProvisioningHandler{}
	h.SetDBEndpointService(stubDBEndpoints{view: view})

	router := chi.NewRouter()
	router.Get("/api/projects/{projectId}/db-endpoint", h.GetDBEndpoint)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/projects/proj-abc1234567/db-endpoint", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func documentDBView() service.DBEndpointView {
	return service.DBEndpointView{
		ProjectID: "proj-abc1234567", Enabled: true, Available: true,
		Host: "proj-abc1234567.db.example.com", Port: 30111, RequireTLS: true,
		Database: "appdb", Username: "appowner",
		MongoPort: 30222, MongoAvailable: true,
		Connection: service.DBEndpointConnectionStrings{
			RequireTLS:          "postgresql://appowner@proj-abc1234567.db.example.com:30111/appdb?sslmode=verify-full",
			AllowPlaintext:      "postgresql://appowner@proj-abc1234567.db.example.com:30111/appdb?sslmode=prefer",
			MongoRequireTLS:     "mongodb://documentdb_admin@proj-abc1234567.db.example.com:30222/?authMechanism=SCRAM-SHA-256&tls=true",
			MongoAllowPlaintext: "mongodb://documentdb_admin@proj-abc1234567.db.example.com:30222/?authMechanism=SCRAM-SHA-256&tls=false",
		},
		Internal: service.DBEndpointInternal{
			Host: "proj-abc1234567-postgres-rw.ns.svc.cluster.local", Port: 5432,
			ConnectionString:      "postgresql://appowner@proj-abc1234567-postgres-rw.ns.svc.cluster.local:5432/appdb?sslmode=prefer",
			MongoPort:             10260,
			MongoConnectionString: "mongodb://documentdb_admin@proj-abc1234567-postgres-rw.ns.svc.cluster.local:10260/?authMechanism=SCRAM-SHA-256&tls=true",
		},
	}
}

func TestDBEndpointResponseCarriesTheMongoPort(t *testing.T) {
	body := endpointBody(t, documentDBView())

	if got, ok := body["mongoPort"].(float64); !ok || int(got) != 30222 {
		t.Errorf("mongoPort: got %v", body["mongoPort"])
	}
	if got, ok := body["mongoAvailable"].(bool); !ok || !got {
		t.Errorf("mongoAvailable: got %v", body["mongoAvailable"])
	}
}

func TestDBEndpointResponseCarriesTheMongoConnectionStrings(t *testing.T) {
	body := endpointBody(t, documentDBView())

	strings_, ok := body["connectionStrings"].(map[string]any)
	if !ok {
		t.Fatalf("connectionStrings: %v", body["connectionStrings"])
	}
	mongoTLS, _ := strings_["mongoRequireTls"].(string)
	if !strings.Contains(mongoTLS, "tls=true") || !strings.HasPrefix(mongoTLS, "mongodb://") {
		t.Errorf("mongoRequireTls: %q", mongoTLS)
	}
	mongoPlain, _ := strings_["mongoAllowPlaintext"].(string)
	if !strings.Contains(mongoPlain, "tls=false") {
		t.Errorf("mongoAllowPlaintext: %q", mongoPlain)
	}
}

// An app hosted beside the database reaches the gateway with no public port,
// so the internal block reports it whether or not the project publishes.
func TestDBEndpointResponseCarriesTheInternalMongoAddress(t *testing.T) {
	body := endpointBody(t, documentDBView())

	internal, ok := body["internal"].(map[string]any)
	if !ok {
		t.Fatalf("internal: %v", body["internal"])
	}
	if got, ok := internal["mongoPort"].(float64); !ok || int(got) != 10260 {
		t.Errorf("internal mongoPort: got %v", internal["mongoPort"])
	}
	if got, _ := internal["mongoConnectionString"].(string); !strings.HasPrefix(got, "mongodb://") {
		t.Errorf("internal mongoConnectionString: %q", got)
	}
}

// A project with no DocumentDB has no Mongo anything, and the response says
// so by omission rather than by a zero a reader has to interpret.
func TestDBEndpointResponseOmitsMongoForAnOrdinaryProject(t *testing.T) {
	view := documentDBView()
	view.MongoPort, view.MongoAvailable = 0, false
	view.Connection.MongoRequireTLS, view.Connection.MongoAllowPlaintext = "", ""
	view.Internal.MongoPort, view.Internal.MongoConnectionString = 0, ""

	body := endpointBody(t, view)

	for _, key := range []string{"mongoPort", "mongoAvailable"} {
		if _, present := body[key]; present {
			t.Errorf("an ordinary project's response carries %s: %v", key, body[key])
		}
	}
	strings_ := body["connectionStrings"].(map[string]any)
	for _, key := range []string{"mongoRequireTls", "mongoAllowPlaintext"} {
		if _, present := strings_[key]; present {
			t.Errorf("an ordinary project's response carries %s", key)
		}
	}
	internal := body["internal"].(map[string]any)
	if _, present := internal["mongoPort"]; present {
		t.Error("an ordinary project's internal block carries mongoPort")
	}
}
