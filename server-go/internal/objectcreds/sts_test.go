package objectcreds

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const assumeRoleResponse = `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
<AssumeRoleResult><Credentials>
<AccessKeyId>TEMPKEY</AccessKeyId><SecretAccessKey>TEMPSECRET</SecretAccessKey>
<SessionToken>TEMPTOKEN</SessionToken><Expiration>2026-09-28T15:00:00Z</Expiration>
</Credentials></AssumeRoleResult></AssumeRoleResponse>`

type assumeRoleCall struct {
	form   url.Values
	signed bool
}

func fakeSTS(t *testing.T, status int, body string) (*httptest.Server, *assumeRoleCall) {
	t.Helper()
	call := &assumeRoleCall{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		call.form, _ = url.ParseQuery(string(raw))
		call.signed = strings.Contains(r.Header.Get("Authorization"), "Credential=parent-key/")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, call
}

type decodedPolicy struct {
	Statement []struct {
		Effect    string
		Action    []string
		Resource  []string
		Condition map[string]map[string][]string
	}
}

func TestSTSMinterAssumesARoleNarrowedToTheProjectPrefix(t *testing.T) {
	server, call := fakeSTS(t, http.StatusOK, assumeRoleResponse)
	parent := Parent{AccessKeyID: "parent-key", SecretAccessKey: "parent-secret", Endpoint: server.URL, Bucket: "backups", Region: "us-east-1"}

	creds, err := STSMinter{}.Mint(context.Background(), parent, Scope{Prefix: "proj1/", Access: ReadWrite, TTL: 12 * time.Hour})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if creds.AccessKeyID != "TEMPKEY" || creds.SecretAccessKey != "TEMPSECRET" || creds.SessionToken != "TEMPTOKEN" {
		t.Fatalf("credentials not taken from the STS answer: %+v", creds)
	}
	if want := time.Date(2026, 9, 28, 15, 0, 0, 0, time.UTC); !creds.ExpiresAt.Equal(want) {
		t.Errorf("expires at %v, want the STS expiration %v", creds.ExpiresAt, want)
	}
	if !call.signed {
		t.Errorf("AssumeRole must be signed with the parent key")
	}
	if call.form.Get("Action") != "AssumeRole" || call.form.Get("DurationSeconds") != "43200" {
		t.Errorf("request = %v", call.form)
	}
	var policy decodedPolicy
	if err := json.Unmarshal([]byte(call.form.Get("Policy")), &policy); err != nil {
		t.Fatalf("session policy: %v (%q)", err, call.form.Get("Policy"))
	}
	var objects, listing bool
	for _, st := range policy.Statement {
		if st.Effect != "Allow" {
			t.Errorf("unexpected effect %q", st.Effect)
		}
		for _, res := range st.Resource {
			switch res {
			case "arn:aws:s3:::backups/proj1/*":
				objects = true
				if strings.Join(st.Action, ",") != "s3:GetObject,s3:PutObject,s3:DeleteObject,s3:AbortMultipartUpload,s3:ListMultipartUploadParts" {
					t.Errorf("read-write object actions = %v", st.Action)
				}
			case "arn:aws:s3:::backups":
				listing = true
				if got := st.Condition["StringLike"]["s3:prefix"]; len(got) != 1 || got[0] != "proj1/*" {
					t.Errorf("listing must be confined to the prefix, got %v", st.Condition)
				}
			default:
				t.Errorf("policy reaches beyond the project: %s", res)
			}
		}
	}
	if !objects || !listing {
		t.Errorf("policy must grant the prefix's objects and a prefix-confined listing: %+v", policy)
	}
}

func TestSTSMinterReadOnlyGrantsNoWrites(t *testing.T) {
	server, call := fakeSTS(t, http.StatusOK, assumeRoleResponse)
	parent := Parent{AccessKeyID: "parent-key", SecretAccessKey: "parent-secret", Endpoint: server.URL, Bucket: "backups"}
	if _, err := (STSMinter{}).Mint(context.Background(), parent, Scope{Prefix: "src/", Access: ReadOnly, TTL: time.Hour}); err != nil {
		t.Fatalf("mint: %v", err)
	}
	policy := call.form.Get("Policy")
	for _, write := range []string{"PutObject", "DeleteObject", "AbortMultipartUpload"} {
		if strings.Contains(policy, write) {
			t.Errorf("read-only policy grants %s: %s", write, policy)
		}
	}
}

func TestSTSMinterSurfacesARefusal(t *testing.T) {
	server, _ := fakeSTS(t, http.StatusForbidden, `<ErrorResponse><Error><Code>AccessDenied</Code><Message>no</Message></Error></ErrorResponse>`)
	parent := Parent{AccessKeyID: "parent-key", SecretAccessKey: "parent-secret", Endpoint: server.URL, Bucket: "backups"}
	_, err := STSMinter{}.Mint(context.Background(), parent, Scope{Prefix: "p/", Access: ReadWrite, TTL: time.Hour})
	if err == nil || strings.Contains(err.Error(), "parent-secret") {
		t.Fatalf("err = %v, want a refusal that never echoes the parent secret", err)
	}
}

func TestSTSMinterRefusesAnUnscopedRequest(t *testing.T) {
	parent := Parent{AccessKeyID: "k", SecretAccessKey: "s", Endpoint: "http://minio:9000", Bucket: "b"}
	cases := []Scope{
		{Prefix: "", Access: ReadWrite, TTL: time.Hour},
		{Prefix: "p/", Access: ReadWrite, TTL: 10 * time.Minute}, // below the STS minimum of 15 minutes
		{Prefix: "p/", Access: ReadWrite, TTL: 8 * 24 * time.Hour},
	}
	for _, scope := range cases {
		if _, err := (STSMinter{}).Mint(context.Background(), parent, scope); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("scope %+v: err = %v, want ErrInvalidRequest", scope, err)
		}
	}
}
