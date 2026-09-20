package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

// The endpoint exists so no client has to carry its own copy of the supported
// set. These tests pin it to the catalogue rather than to a literal list: when
// the catalogue changes, the endpoint changes with it and nothing here needs
// editing.

func listCatalog(t *testing.T) postgresCatalogDTO {
	t.Helper()
	rec := httptest.NewRecorder()
	NewPostgresCatalogHandler().List(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", rec.Code, rec.Body.String())
	}
	var out postgresCatalogDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestPostgresCatalogHandler_ListsEveryCatalogueMajor(t *testing.T) {
	out := listCatalog(t)

	want := config.PostgresMajors()
	if len(out.Majors) != len(want) {
		t.Fatalf("majors: got %d entries, want %d (%v)", len(out.Majors), len(want), want)
	}
	for i, major := range want {
		if out.Majors[i].Major != major {
			t.Errorf("major %d: got %q, want %q", i, out.Majors[i].Major, major)
		}
	}
}

func TestPostgresCatalogHandler_DocumentDBMirrorsTheCatalogue(t *testing.T) {
	for _, entry := range listCatalog(t).Majors {
		if entry.DocumentDB != config.DocumentDBSupported(entry.Major) {
			t.Errorf("major %s: documentDb %v, catalogue says %v",
				entry.Major, entry.DocumentDB, config.DocumentDBSupported(entry.Major))
		}
	}
}

// A major that cannot carry DocumentDB has to say why over the wire. The
// reason is what a customer reads before choosing, so it cannot live in a
// client that would then be free to disagree with the catalogue.
func TestPostgresCatalogHandler_UnavailableDocumentDBCarriesAReason(t *testing.T) {
	for _, entry := range listCatalog(t).Majors {
		switch {
		case entry.DocumentDB && entry.DocumentDBUnavailableReason != "":
			t.Errorf("major %s: supports DocumentDB but carries reason %q", entry.Major, entry.DocumentDBUnavailableReason)
		case !entry.DocumentDB && entry.DocumentDBUnavailableReason == "":
			t.Errorf("major %s: no DocumentDB and no reason given", entry.Major)
		}
	}
}

// 18 is deliberately absent from the catalogue. If it ever appears here it
// appeared because someone added it, not because a version number moved.
func TestPostgresCatalogHandler_DoesNotOfferUnsupportedMajors(t *testing.T) {
	for _, entry := range listCatalog(t).Majors {
		if entry.Major == "18" {
			t.Error("18 is offered; it is deliberately not in the catalogue")
		}
	}
}

func TestPostgresCatalogHandler_ReportsWhetherAMajorIsProvisionable(t *testing.T) {
	for _, entry := range listCatalog(t).Majors {
		_, err := config.PostgresImage(entry.Major)
		if entry.Available != (err == nil) {
			t.Errorf("major %s: available=%v but PostgresImage err=%v", entry.Major, entry.Available, err)
		}
	}
}

func TestPostgresCatalogHandler_NeverExposesImageReferences(t *testing.T) {
	rec := httptest.NewRecorder()
	NewPostgresCatalogHandler().List(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, entry := range raw["majors"].([]any) {
		for key := range entry.(map[string]any) {
			if key == "image" || key == "baseImage" {
				t.Errorf("catalogue endpoint leaks %q to tenants", key)
			}
		}
	}
}
