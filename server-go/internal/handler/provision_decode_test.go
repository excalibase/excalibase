package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-555: a provision body naming a field the platform does not read is
// refused by name instead of being silently ignored.
func TestProvision_RefusesUnknownAndUnimplementedFieldsByName(t *testing.T) {
	r := provisionRouter(t, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}})
	for field, body := range map[string]string{
		"tier":               `{"projectName":"a","orgId":"org1","databaseType":"POSTGRESQL","postgresVersion":"17","tier":"ENTERPRISE"}`,
		"network":            `{"projectName":"a","orgId":"org1","databaseType":"POSTGRESQL","postgresVersion":"17","network":{"policyEnabled":true}}`,
		"maintenance":        `{"projectName":"a","orgId":"org1","databaseType":"POSTGRESQL","postgresVersion":"17","maintenance":{"window":"sun"}}`,
		"pooler":             `{"projectName":"a","orgId":"org1","databaseType":"POSTGRESQL","postgresVersion":"17","pooler":{"enabled":true}}`,
		"webhookUrl":         `{"projectName":"a","orgId":"org1","databaseType":"POSTGRESQL","postgresVersion":"17","webhookUrl":"http://x"}`,
		"parameterGroupName": `{"projectName":"a","orgId":"org1","databaseType":"POSTGRESQL","postgresVersion":"17","parameterGroupName":"g"}`,
	} {
		w := doRequest(r, "POST", testProvisionPath, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (body=%s)", field, w.Code, w.Body.String())
			continue
		}
		if !strings.Contains(w.Body.String(), field) {
			t.Errorf("%s: refusal does not name the field: %s", field, w.Body.String())
		}
	}
}

// The body Studio's create-project page sends still provisions.
func TestProvision_AcceptsTheStudioBody(t *testing.T) {
	r := provisionRouter(t, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}})
	w := doRequest(r, "POST", testProvisionPath,
		`{"projectName":"blog","orgId":"org1","databaseType":"POSTGRESQL","postgresVersion":"17","documentDb":false}`)
	if w.Code != http.StatusOK && w.Code != http.StatusAccepted {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
}

func TestProvision_RefusesAnUnsafeMasterUsername(t *testing.T) {
	r := provisionRouter(t, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}})
	w := doRequest(r, "POST", testProvisionPath,
		`{"projectName":"a","orgId":"org1","databaseType":"POSTGRESQL","postgresVersion":"17","masterUsername":"all 0.0.0.0/0 trust #"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "masterUsername") {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
}

func TestProvision_RefusesABadTagNamingIt(t *testing.T) {
	r := provisionRouter(t, &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{}})
	w := doRequest(r, "POST", testProvisionPath,
		`{"projectName":"a","orgId":"org1","databaseType":"POSTGRESQL","postgresVersion":"17","tags":{"bad key":"v"}}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "bad key") {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
}
