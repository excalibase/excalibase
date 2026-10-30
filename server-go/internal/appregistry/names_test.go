package appregistry

import "testing"

func TestRepository_IsTheProjectThenTheApp(t *testing.T) {
	got, err := Repository("proj-abc123defg", "api")
	if err != nil || got != "proj-abc123defg/api" {
		t.Fatalf("repository %q: %v", got, err)
	}
	for _, bad := range [][2]string{{"Proj_Upper", "api"}, {"proj-x", "API"}, {"", "api"}, {"proj-x", ""}, {"proj-x", "a/b"}, {"../x", "api"}} {
		if _, err := Repository(bad[0], bad[1]); err == nil {
			t.Errorf("%v: a name the registry or another project could read as its own", bad)
		}
	}
}

func TestOwnedBy_OnlyTheProjectsOwnRepositories(t *testing.T) {
	cases := map[string]bool{
		"proj-a/api": true, "proj-a/web/x": false, "proj-ab/api": false, "proj-b/api": false,
		"proj-a": false, "proj-a/": false, "proj-a/API": false, "proj-a/../proj-b/api": false,
	}
	for repo, want := range cases {
		if got := OwnedBy(repo, "proj-a"); got != want {
			t.Errorf("%s owned by proj-a: %v, want %v", repo, got, want)
		}
	}
}
