package sites

import "testing"

// The flagged queries are spelled out as single literals so the analyzer sees
// constant text; these cases pin each one to the shared column list it must
// match, so a column added to one list cannot drift from its scan order.
func TestQueryTextMatchesSharedColumns(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"listDeployTokensSQL", listDeployTokensSQL, `SELECT ` + tokenColumns + ` FROM site_deploy_tokens
		WHERE project_id = $1 AND site_id = $2 ORDER BY created_at, id`},
		{"findDeployTokenSQL", findDeployTokenSQL, `SELECT ` + tokenColumns + ` FROM site_deploy_tokens WHERE token_hash = $1`},
		{"getHostnameSQL", getHostnameSQL, `SELECT ` + hostnameColumns + `
		FROM site_hostnames WHERE project_id = $1 AND site_id = $2 AND id = $3`},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s drifted from its column list:\n got: %q\nwant: %q", tc.name, tc.got, tc.want)
		}
	}
}
