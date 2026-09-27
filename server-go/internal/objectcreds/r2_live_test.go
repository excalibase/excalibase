//go:build live

package objectcreds

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// Run with R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, R2_ENDPOINT and R2_BUCKET set:
// go test ./internal/objectcreds/ -tags=live -run TestLiveR2 -v -count=1
func TestLiveR2TemporaryCredentialsStayInsideTheirPrefix(t *testing.T) {
	parent := Parent{
		AccessKeyID: os.Getenv("R2_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("R2_SECRET_ACCESS_KEY"),
		Endpoint: os.Getenv("R2_ENDPOINT"), Bucket: os.Getenv("R2_BUCKET"),
	}
	if parent.AccessKeyID == "" || parent.Endpoint == "" || parent.Bucket == "" {
		t.Skip("R2_* not set")
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	own := "exc476-live-" + hex.EncodeToString(suffix) + "/"
	other := "exc476-live-other-" + hex.EncodeToString(suffix) + "/"
	ctx := context.Background()

	writer := mint(t, parent, Scope{Prefix: own, Access: ReadWrite, TTL: 10 * time.Minute})
	put(t, writer, parent, own+"a.txt", nil)
	put(t, writer, parent, other+"a.txt", errAccessDenied)
	if got := get(t, writer, parent, own+"a.txt"); got != "payload" {
		t.Fatalf("read back %q", got)
	}
	list(t, writer, parent, own, nil)
	list(t, writer, parent, "", errAccessDenied)

	reader := mint(t, parent, Scope{Prefix: own, Access: ReadOnly, TTL: 10 * time.Minute})
	if got := get(t, reader, parent, own+"a.txt"); got != "payload" {
		t.Fatalf("read-only read back %q", got)
	}
	put(t, reader, parent, own+"b.txt", errAccessDenied)

	shortLived := mint(t, parent, Scope{Prefix: own, Access: ReadWrite, TTL: 2 * time.Second})
	time.Sleep(4 * time.Second)
	put(t, shortLived, parent, own+"late.txt", errAccessDenied)

	_, err := client(writer, parent).DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(parent.Bucket), Key: aws.String(own + "a.txt")})
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

var errAccessDenied = errors.New("access denied")

func mint(t *testing.T, parent Parent, scope Scope) Credentials {
	t.Helper()
	creds, err := R2Signer{}.Mint(context.Background(), parent, scope)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return creds
}

func client(creds Credentials, parent Parent) *s3.Client {
	return s3.New(s3.Options{
		Region:       "auto",
		BaseEndpoint: aws.String(parent.Endpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken),
	})
}

func expect(t *testing.T, what string, err, want error) {
	t.Helper()
	if want == nil {
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		return
	}
	// R2 answers 403 for a key outside the scope and for an expired one.
	var respErr *smithyhttp.ResponseError
	if !errors.As(err, &respErr) || respErr.HTTPStatusCode() != http.StatusForbidden {
		t.Fatalf("%s: err = %v, want 403", what, err)
	}
}

func put(t *testing.T, creds Credentials, parent Parent, key string, want error) {
	t.Helper()
	_, err := client(creds, parent).PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(parent.Bucket), Key: aws.String(key), Body: strings.NewReader("payload"),
	})
	expect(t, "put "+key, err, want)
}

func get(t *testing.T, creds Credentials, parent Parent, key string) string {
	t.Helper()
	out, err := client(creds, parent).GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(parent.Bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	defer out.Body.Close()
	body, _ := io.ReadAll(out.Body)
	return string(body)
}

func list(t *testing.T, creds Credentials, parent Parent, prefix string, want error) {
	t.Helper()
	_, err := client(creds, parent).ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{Bucket: aws.String(parent.Bucket), Prefix: aws.String(prefix)})
	expect(t, "list "+prefix, err, want)
}
