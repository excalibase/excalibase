package postgres

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
		{"findUserByIDSQL", findUserByIDSQL, `SELECT ` + userColumns + ` FROM users WHERE id = $1`},
		{"findUserByUsernameSQL", findUserByUsernameSQL, `SELECT ` + userColumns + ` FROM users WHERE lower(username) = lower($1)`},
		{"findUserByEmailSQL", findUserByEmailSQL, `SELECT ` + userColumns + ` FROM users WHERE lower(email) = lower($1)
		 ORDER BY kind = 'human' DESC LIMIT 1`},
		{"findAllUsersSQL", findAllUsersSQL, `SELECT ` + userColumns + ` FROM users`},
		{"findUserByIdentitySQL", findUserByIdentitySQL, `SELECT ` + prefixedUserColumns + ` FROM users u JOIN studio_identities i ON i.user_id = u.id
		 WHERE i.provider = $1 AND i.subject = $2`},
		{"findTokenByHashSQL", findTokenByHashSQL, `SELECT ` + tokenColumns + ` FROM access_tokens WHERE token_hash = $1`},
		{"listTokensByUserSQL", listTokensByUserSQL, `SELECT ` + tokenColumns + ` FROM access_tokens WHERE user_id = $1`},
		{"listTokensByProjectSQL", listTokensByProjectSQL, `SELECT ` + tokenColumns + ` FROM access_tokens WHERE project_id = $1 ORDER BY created_at, token_hash`},
		{"findRestoreJobSQL", findRestoreJobSQL, restoreJobColumns + `
		WHERE id = $1 AND ($2 IN (source_project_id, new_project_id))`},
		{"insertLogDrainSQL", insertLogDrainSQL, `INSERT INTO log_drains (` + logDrainColumns + `)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,0,0,$13,NULL,0,0,$14,$15,$16)`},
		{"findPendingInviteSQL", findPendingInviteSQL, `SELECT ` + pendingInviteColumns + ` FROM pending_invites WHERE token_hash = $1 AND expires_at > $2`},
		{"acceptPendingInviteSQL", acceptPendingInviteSQL, `DELETE FROM pending_invites WHERE token_hash = $1 AND expires_at > $2 RETURNING ` + pendingInviteColumns},
		{"listPendingInvitesSQL", listPendingInvitesSQL, `SELECT ` + pendingInviteColumns + ` FROM pending_invites WHERE org_id = $1`},
		{"findInstanceByProjectIDSQL", findInstanceByProjectIDSQL, `SELECT` + pgInstanceColumns + `
	FROM database_instances WHERE project_id = $1`},
		{"findAllInstancesSQL", findAllInstancesSQL, `SELECT` + pgInstanceColumns + `
	FROM database_instances`},
		{"findInstancesByOwnerSQL", findInstancesByOwnerSQL, `SELECT` + pgInstanceColumns + `
	FROM database_instances WHERE owner_id = $1`},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s drifted from its column list:\n got: %q\nwant: %q", tc.name, tc.got, tc.want)
		}
	}
}
