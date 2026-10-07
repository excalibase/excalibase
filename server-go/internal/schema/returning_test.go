package schema

import "testing"

func TestReturnsRowsOnlyForAWriteWithARealReturningClause(t *testing.T) {
	cases := map[string]bool{
		"INSERT INTO t (a) VALUES (1) RETURNING id":                                                   true,
		"  update t set a = 1\nreturning *":                                                           true,
		"delete from t where id = 1 returning id, a":                                                  true,
		"MERGE INTO t USING s ON t.id = s.id WHEN MATCHED THEN DELETE RETURNING t.id":                 true,
		"INSERT INTO t (a) VALUES ('returning')":                                                      false,
		`INSERT INTO t ("returning") VALUES (1)`:                                                      false,
		"INSERT INTO t (a) VALUES ('it''s returning')":                                                false,
		"INSERT INTO t (a) VALUES (1) -- returning id":                                                false,
		"INSERT INTO t (a) VALUES (1) /* returning id */":                                             false,
		"INSERT INTO t (a) VALUES ($$ returning $$)":                                                  false,
		"INSERT INTO t (a) VALUES ($tag$ returning $tag$) RETURNING a":                                true,
		"INSERT INTO t (a) VALUES ('unterminated returning":                                           false,
		"CREATE FUNCTION f() RETURNS void AS $$ INSERT INTO t VALUES (1) RETURNING 1 $$ LANGUAGE sql": false,
		"SELECT 1":                    false,
		"UPDATE t SET a = 1":          false,
		"INSERT INTO t VALUES (1) $1": false,
	}
	for query, want := range cases {
		if got := returnsRows(query); got != want {
			t.Errorf("returnsRows(%q) = %v, want %v", query, got, want)
		}
	}
}
