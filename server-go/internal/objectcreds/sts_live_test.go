//go:build live

package objectcreds

import (
	"os"
	"testing"
	"time"
)

// Run against a MinIO whose root (or any) user is in STS_PARENT_KEY/STS_PARENT_SECRET:
// go test ./internal/objectcreds/ -tags=live -run TestLiveSTS -v -count=1
func TestLiveSTSCredentialsStayInsideTheirPrefix(t *testing.T) {
	parent := Parent{
		AccessKeyID: os.Getenv("STS_PARENT_KEY"), SecretAccessKey: os.Getenv("STS_PARENT_SECRET"),
		Endpoint: os.Getenv("STS_ENDPOINT"), Bucket: os.Getenv("STS_BUCKET"), Region: "us-east-1",
	}
	if parent.AccessKeyID == "" {
		t.Skip("STS_* not set")
	}
	mintWith := func(scope Scope) Credentials {
		creds, err := STSMinter{}.Mint(t.Context(), parent, scope)
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		return creds
	}
	writer := mintWith(Scope{Prefix: "own/", Access: ReadWrite, TTL: 15 * time.Minute})
	put(t, writer, parent, "own/a.txt", nil)
	put(t, writer, parent, "other/a.txt", errAccessDenied)
	if got := get(t, writer, parent, "own/a.txt"); got != "payload" {
		t.Fatalf("read back %q", got)
	}
	list(t, writer, parent, "own/", nil)
	list(t, writer, parent, "", errAccessDenied)
	reader := mintWith(Scope{Prefix: "own/", Access: ReadOnly, TTL: 15 * time.Minute})
	put(t, reader, parent, "own/b.txt", errAccessDenied)
	if got := get(t, reader, parent, "own/a.txt"); got != "payload" {
		t.Fatalf("read-only read back %q", got)
	}
}
