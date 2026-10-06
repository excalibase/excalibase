package service

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

func supportedMajor(t *testing.T) string {
	t.Helper()
	majors := config.PostgresMajors()
	if len(majors) == 0 {
		t.Skip("no postgres major in the catalogue")
	}
	return majors[0]
}

// EXC-555: the owner name is written into pg_hba trust lines, so a name that
// adds tokens there would hand out passwordless access.
func TestValidateRefusesAnUnsafeMasterUsername(t *testing.T) {
	for _, name := range []string{"all 0.0.0.0/0 trust #", "postgres", "pg_read_all_data", "Owner"} {
		req := provisionRequest(supportedMajor(t))
		req.MasterUsername = name
		err := validateProvisioningRequest(req)
		if err == nil || !strings.Contains(err.Error(), "masterUsername") {
			t.Errorf("%q: got %v, want a masterUsername refusal", name, err)
		}
	}
	req := provisionRequest(supportedMajor(t))
	req.MasterUsername = "owner_doc"
	if err := validateProvisioningRequest(req); err != nil {
		t.Errorf("plain owner refused: %v", err)
	}
}

func TestValidateRefusesTagsThatAreNotKubernetesLabels(t *testing.T) {
	for key, value := range map[string]string{
		"bad key":               "v",
		"team":                  "has space",
		"":                      "v",
		"cnpg.io/cluster":       "x",
		"kubernetes.io/name":    "x",
		strings.Repeat("k", 64): "v",
		"ok":                    strings.Repeat("v", 64),
	} {
		req := provisionRequest(supportedMajor(t))
		req.Tags = map[string]string{key: value}
		err := validateProvisioningRequest(req)
		if err == nil || !strings.Contains(err.Error(), "tag") {
			t.Errorf("tag %q=%q: got %v, want a tag refusal", key, value, err)
		}
	}
	req := provisionRequest(supportedMajor(t))
	req.Tags = map[string]string{"team": "billing", "example.com/env": "prod-1", "empty": ""}
	if err := validateProvisioningRequest(req); err != nil {
		t.Errorf("valid tags refused: %v", err)
	}
}

func TestValidateCountsProjectNameInCharacters(t *testing.T) {
	req := provisionRequest(supportedMajor(t))
	req.ProjectName = strings.Repeat("é", 100)
	if err := validateProvisioningRequest(req); err != nil {
		t.Errorf("100 characters refused: %v", err)
	}
	req.ProjectName = strings.Repeat("a", 101)
	if err := validateProvisioningRequest(req); err == nil {
		t.Error("101 characters accepted")
	}
}
