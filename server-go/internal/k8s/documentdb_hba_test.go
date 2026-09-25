package k8s

import (
	"slices"
	"strings"
	"testing"
)

func hbaLines(t *testing.T, opts PostgreSQLClusterOpts) []string {
	t.Helper()
	raw := postgresqlSection(t, opts)["pg_hba"].([]interface{})
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		lines = append(lines, line.(string))
	}
	return lines
}

func documentDBOpts(owner string) PostgreSQLClusterOpts {
	return PostgreSQLClusterOpts{
		ProjectID:      "proj-hba00001",
		Namespace:      "org-a-proj-hba00001",
		Tier:           documentDBTier(),
		DatabaseName:   "appdb",
		MasterUsername: owner,
		DocumentDB:     true,
	}
}

func TestDocumentDBTrustsTheGatewayRolesOnLoopbackOnly(t *testing.T) {
	lines := hbaLines(t, documentDBOpts("owner_doc"))

	var trusted []string
	for _, line := range lines {
		if strings.HasSuffix(line, " trust") {
			trusted = append(trusted, line)
		}
	}
	want := []string{
		"host all documentdb 127.0.0.1/32 trust",
		"host all documentdb ::1/128 trust",
		"host all owner_doc 127.0.0.1/32 trust",
		"host all owner_doc ::1/128 trust",
		"host all excalibase_app 127.0.0.1/32 trust",
		"host all excalibase_app ::1/128 trust",
	}
	if !slices.Equal(trusted, want) {
		t.Errorf("trust lines:\n got %q\nwant %q", trusted, want)
	}
}

// pg_hba takes the first matching line, so the loopback trust must precede the password lines.
func TestDocumentDBTrustLinesComeBeforeThePasswordLines(t *testing.T) {
	lines := hbaLines(t, documentDBOpts(""))

	lastTrust, firstPassword := -1, len(lines)
	for i, line := range lines {
		if strings.HasSuffix(line, " trust") {
			lastTrust = i
		}
		if strings.HasSuffix(line, " scram-sha-256") && i < firstPassword {
			firstPassword = i
		}
	}
	if lastTrust < 0 || lastTrust > firstPassword {
		t.Errorf("trust lines are not first: %q", lines)
	}
	if !slices.Contains(lines, "host all app 127.0.0.1/32 trust") {
		t.Errorf("the default owner is not trusted on loopback: %q", lines)
	}
}

func TestAPlainProjectTrustsNobody(t *testing.T) {
	opts := documentDBOpts("owner_doc")
	opts.DocumentDB = false
	for _, line := range hbaLines(t, opts) {
		if strings.Contains(line, "trust") {
			t.Errorf("a non-DocumentDB project trusts %q", line)
		}
	}
}
