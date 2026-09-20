package service

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-408: the major comes from the request. There is no implicit default, so
// a request that names none is refused rather than quietly provisioned on
// whatever the code happened to hardcode.

func provisionRequest(version string) domain.ProvisioningRequest {
	return domain.ProvisioningRequest{
		ProjectName:     "blog",
		OrgID:           "org1",
		DBType:          domain.PostgreSQL,
		Tier:            domain.Free,
		PostgresVersion: version,
	}
}

func TestValidateRefusesAMissingPostgresMajor(t *testing.T) {
	for _, version := range []string{"", "   "} {
		err := validateProvisioningRequest(provisionRequest(version))
		if err == nil {
			t.Fatalf("version %q: expected a refusal, got nil", version)
		}
		if !strings.Contains(err.Error(), "postgres version") {
			t.Errorf("version %q: error should mention the postgres version, got %q", version, err)
		}
	}
}

func TestValidateRefusesAnUnsupportedPostgresMajor(t *testing.T) {
	for _, version := range []string{"9.2", "13", "19", "17.2", "latest"} {
		err := validateProvisioningRequest(provisionRequest(version))
		if err == nil {
			t.Errorf("version %q: expected a refusal, got nil", version)
		}
	}
}

func TestValidateAcceptsEverySupportedMajor(t *testing.T) {
	for _, major := range config.PostgresMajors() {
		if err := validateProvisioningRequest(provisionRequest(major)); err != nil {
			t.Errorf("major %s: expected acceptance, got %v", major, err)
		}
	}
}

func TestValidateRefusalNamesTheSupportedMajors(t *testing.T) {
	err := validateProvisioningRequest(provisionRequest("13"))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, major := range config.PostgresMajors() {
		if !strings.Contains(err.Error(), major) {
			t.Errorf("refusal should list supported major %s, got %q", major, err)
		}
	}
}

// EXC-407 item 5: DocumentDB is refused at the API on a major whose image does
// not carry it, rather than failing when the extension is installed.
func TestValidateRefusesDocumentDBOnAMajorThatCannotOfferIt(t *testing.T) {
	for _, major := range config.PostgresMajors() {
		if config.DocumentDBSupported(major) {
			continue
		}
		req := provisionRequest(major)
		req.DocumentDB = true
		err := validateProvisioningRequest(req)
		if err == nil {
			t.Errorf("major %s: DocumentDB must be refused", major)
			continue
		}
		if !strings.Contains(strings.ToLower(err.Error()), "documentdb") {
			t.Errorf("major %s: refusal should mention DocumentDB, got %q", major, err)
		}
	}
}

func TestValidateAcceptsDocumentDBOnASupportedMajor(t *testing.T) {
	for _, major := range config.PostgresMajors() {
		if !config.DocumentDBSupported(major) {
			continue
		}
		req := provisionRequest(major)
		req.DocumentDB = true
		if err := validateProvisioningRequest(req); err != nil {
			t.Errorf("major %s: DocumentDB should be accepted, got %v", major, err)
		}
	}
}

func TestProvisionRefusesARequestWithNoPostgresMajor(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	_, err := svc.Provision(context.Background(), provisionRequest(""))
	if err == nil || !strings.Contains(err.Error(), "postgres version") {
		t.Errorf("expected a postgres version refusal, got: %v", err)
	}
}

func TestUpgradeVersionRefusesAnUnsupportedMajor(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	err := svc.UpgradeVersion(context.Background(), "proj-1", "13")
	if err == nil {
		t.Fatal("UpgradeVersion accepted an unsupported major")
	}
	if !strings.Contains(err.Error(), "13") {
		t.Errorf("error should name the rejected major, got %q", err)
	}
}

func TestUpgradeVersionRefusesAnEmptyMajor(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	if err := svc.UpgradeVersion(context.Background(), "proj-1", ""); err == nil {
		t.Fatal("UpgradeVersion accepted an empty major")
	}
}

// The Docker restore path must reuse the source project's major rather than
// falling back to a hardcoded one.
func TestRestoreImageHonoursTheSourceMajor(t *testing.T) {
	for _, major := range config.PostgresMajors() {
		image, err := restoreImage(major)
		if err != nil {
			t.Errorf("major %s: %v", major, err)
			continue
		}
		if image != "postgres:"+major {
			t.Errorf("major %s: got %q", major, image)
		}
	}
}

func TestRestoreImageRefusesAnUnknownMajor(t *testing.T) {
	for _, major := range []string{"", "13", "latest"} {
		if _, err := restoreImage(major); err == nil {
			t.Errorf("major %q: restoreImage must refuse rather than default", major)
		}
	}
}
