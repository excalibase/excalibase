package appregistry

import (
	"strings"
	"testing"
	"time"
)

func TestPushTag_CarriesTheTimeAndTheDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	at := time.Unix(1_800_000_000, 0)
	tag, err := PushTag(digest, at)
	if err != nil || len(tag) > 128 {
		t.Fatalf("%q %v", tag, err)
	}
	gotDigest, gotAt, ok := ParsePushTag(tag)
	if !ok || gotDigest != digest || !gotAt.Equal(at) {
		t.Fatalf("round trip: %s %s %v", gotDigest, gotAt, ok)
	}
	if _, err := PushTag("sha256:short", at); err == nil {
		t.Error("a tag for what is not a sha256 digest")
	}
	for _, tag := range []string{"latest", "p-1-" + strings.Repeat("a", 64), "p1800000000-" + strings.Repeat("g", 64), "p1800000000-" + strings.Repeat("a", 63), "p99999999999999999999-" + strings.Repeat("a", 64)} {
		if _, _, ok := ParsePushTag(tag); ok {
			t.Errorf("%q read as a push tag", tag)
		}
	}
}

func TestSplitRepository_OnlyAProjectAndAnApp(t *testing.T) {
	if project, app, ok := SplitRepository("proj-a/api"); !ok || project != "proj-a" || app != "api" {
		t.Fatalf("%s %s %v", project, app, ok)
	}
	for _, repo := range []string{"proj-a", "proj-a/api/x", "Proj/api", "../x", "proj-a/", "/api"} {
		if _, _, ok := SplitRepository(repo); ok {
			t.Errorf("%q split", repo)
		}
	}
}
