package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-409: the project API says whether the project's database carries
// DocumentDB, so a client renders a Mongo surface only when there is one.
//
// The answer is always present, both ways round. A client that had to infer
// "no" from a missing field could not tell an ordinary project from an older
// server, and would either hide the surface on a project that has it or offer
// it on one that does not.

// readDocumentDBFlag fetches a project and returns its documentDb field and
// whether the field was present at all.
func readDocumentDBFlag(t *testing.T, r http.Handler, projectID string) (bool, bool) {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/provision/"+projectID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, body %s", projectID, rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	value, present := body["documentDb"]
	if !present {
		return false, false
	}
	flag, ok := value.(bool)
	if !ok {
		t.Fatalf("documentDb is %T, want a boolean", value)
	}
	return flag, true
}

// seedDocumentDBProjects registers one project of each kind.
func seedDocumentDBProjects(t *testing.T, store *storage.FileSystemStore) {
	t.Helper()
	for _, project := range []struct {
		id         string
		documentDB bool
	}{{"doc-db", true}, {"plain-db", false}} {
		err := store.Create(&domain.DatabaseInstance{
			ProjectID: project.id, OrgID: "org1", DBType: domain.PostgreSQL,
			Tier: domain.Free, Namespace: "org1-" + project.id, Status: "ACTIVE",
			DatabaseName: "appdb", PostgresVersion: "17", DocumentDB: project.documentDB,
		})
		if err != nil {
			t.Fatalf("seed %s: %v", project.id, err)
		}
	}
}

func TestProjectAPIReportsADocumentDBProject(t *testing.T) {
	r, store, _ := fullRouter(t)
	seedDocumentDBProjects(t, store)

	flag, present := readDocumentDBFlag(t, r, "doc-db")
	if !present {
		t.Fatal("the project payload carries no documentDb field")
	}
	if !flag {
		t.Error("documentDb is false for a project created with DocumentDB")
	}
}

func TestProjectAPIReportsAnOrdinaryProjectAsNotDocumentDB(t *testing.T) {
	r, store, _ := fullRouter(t)
	seedDocumentDBProjects(t, store)

	flag, present := readDocumentDBFlag(t, r, "plain-db")
	if !present {
		t.Fatal("the project payload omits documentDb rather than answering no")
	}
	if flag {
		t.Error("documentDb is true for a project created without DocumentDB")
	}
}
