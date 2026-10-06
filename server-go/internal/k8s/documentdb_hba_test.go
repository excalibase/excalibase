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
		"host all +excalibase_mongo_users 127.0.0.1/32 trust",
		"host all +excalibase_mongo_users ::1/128 trust",
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

// A project's Mongo users (EXC-427) log in through the gateway only: the
// loopback trust admits them from inside the pod, and one reject line refuses
// them over any network before CNPG's catch-all password line could admit them.
// It is "host", so it matches TLS and plaintext connections alike.
func TestAMongoUserIsRefusedEveryNetworkLogin(t *testing.T) {
	lines := hbaLines(t, documentDBOpts("owner_doc"))
	const reject = "host all +excalibase_mongo_users all reject"

	at := slices.Index(lines, reject)
	if at < 0 {
		t.Fatalf("no network reject for Mongo users: %q", lines)
	}
	for i, line := range lines {
		if strings.Contains(line, "+excalibase_mongo_users") && strings.HasSuffix(line, " trust") && i > at {
			t.Errorf("loopback trust %q comes after the reject and would never match", line)
		}
		if strings.HasSuffix(line, " scram-sha-256") && i < at {
			t.Errorf("password line %q precedes the Mongo user reject", line)
		}
	}
}

// EXC-555 defence in depth: an owner that is not a plain role name never
// reaches a trust or ident line, even if the request boundary were bypassed.
func TestAnUnsafeOwnerNeverReachesPgHBAOrIdent(t *testing.T) {
	for _, owner := range []string{"all 0.0.0.0/0 trust #", "postgres", "Owner", "app\nhost"} {
		opts := documentDBOpts(owner)
		for _, line := range hbaLines(t, opts) {
			if strings.Contains(line, "all "+owner+" ") {
				t.Errorf("owner %q reached pg_hba: %q", owner, line)
			}
		}
		for _, line := range postgresqlSection(t, opts)["pg_ident"].([]interface{}) {
			if strings.HasSuffix(line.(string), "postgres "+owner) {
				t.Errorf("owner %q reached pg_ident: %q", owner, line)
			}
		}
	}
}

func TestAPlainProjectHasNoMongoUserLines(t *testing.T) {
	opts := documentDBOpts("owner_doc")
	opts.DocumentDB = false
	for _, line := range hbaLines(t, opts) {
		if strings.Contains(line, "excalibase_mongo_users") {
			t.Errorf("a non-DocumentDB project names Mongo users: %q", line)
		}
	}
}
