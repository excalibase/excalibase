package service

import (
	"strings"
	"testing"
)

func TestProjectRoleSQL_KeepsPublishedTablesAtFullIdentity(t *testing.T) {
	sql := projectRoleSQL(projectRoleSpec{databaseName: "app", adminUsername: "app"}, projectRoleCredentials{}, "rt_pub")
	for _, want := range []string{
		"DROP ROUTINE IF EXISTS excalibase.keep_realtime_identity();",
		"CREATE EVENT TRIGGER excalibase_keep_realtime_identity ON ddl_command_end WHEN TAG IN ('ALTER TABLE')",
		"target_publication CONSTANT text := " + sqlTextLiteral("rt_pub") + ";",
		"WHERE p.pubname = " + sqlTextLiteral("rt_pub") + " AND c.relkind = 'r' AND c.relreplident <> 'f'",
		"DROP ROUTINE IF EXISTS excalibase.set_realtime_table(text, text, boolean);",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(realtimeIdentitySQL("rt_pub"), "SECURITY DEFINER") {
		t.Error("the identity guard runs with its definer's rights")
	}
}

func TestRealtimeIdentitySQL_DefaultsAndQuotesThePublication(t *testing.T) {
	if !strings.Contains(realtimeIdentitySQL(""), sqlTextLiteral(DefaultPublicationName)) {
		t.Error("an empty publication name does not fall back to the default")
	}
	if strings.Contains(realtimeIdentitySQL("x'; DROP TABLE t; --"), "DROP TABLE t") {
		t.Error("the publication name reached the SQL unquoted")
	}
}
