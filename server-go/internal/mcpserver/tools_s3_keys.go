package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// S3 access keys (EXC-630): a coding tool may see which keys a project holds
// and revoke one. Issuing a key mints a credential, which ADR 0037 keeps in
// Studio, so there is no create tool.

var s3KeyIDPattern = regexp.MustCompile(`^s3k_[0-9a-f]{32}$`)

type s3KeyArgs struct {
	projectArg
	confirmArg
	KeyID string `json:"key_id" jsonschema:"the key's id (s3k_...), from list_s3_keys; not its access key id"`
}

type s3KeyPermissions struct {
	Read   bool `json:"read"`
	Write  bool `json:"write"`
	Delete bool `json:"delete"`
}

// listedS3Key is what a tool shows of a key: never a secret, whatever the API sent.
type listedS3Key struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	AccessKeyID string           `json:"accessKeyId"`
	Buckets     []string         `json:"buckets"`
	Permissions s3KeyPermissions `json:"permissions"`
	CreatedAt   *time.Time       `json:"createdAt,omitempty"`
	ExpiresAt   *time.Time       `json:"expiresAt,omitempty"`
	LastUsedAt  *time.Time       `json:"lastUsedAt,omitempty"`
}

type storedS3Key struct {
	listedS3Key
	Read   bool `json:"read"`
	Write  bool `json:"write"`
	Delete bool `json:"delete"`
}

const s3KeysNote = "An S3 key lets any S3 client (aws cli, rclone, AWS SDKs) reach the project's buckets at the endpoint, " +
	"path-style. Buckets empty means every bucket. New keys are made in Studio (Storage, S3 access); the secret is shown once there."

func s3KeyTools() []entry {
	return []entry{
		dark(features.S3, tool("list_s3_keys", "The project's S3 access keys: names, access key ids, buckets, permissions and last use, "+
			"with the endpoint and region S3 clients use. Never a secret.", readTool, listS3Keys)),
		dark(features.S3, tool("revoke_s3_key", "Revoke one S3 access key at once; every client using it is refused from the next request."+confirmNote, writeTool, revokeS3Key)),
	}
}

type storedS3Keys struct {
	Keys     []storedS3Key `json:"keys"`
	Endpoint string        `json:"endpoint"`
	Region   string        `json:"region"`
}

func getS3Keys(ctx context.Context, c *call, projectID string) (storedS3Keys, error) {
	var listed storedS3Keys
	err := c.get(ctx, projectsAPI+projectID+"/storage/s3-keys", nil, &listed)
	return listed, err
}

func bucketScope(buckets []string) string {
	if len(buckets) == 0 {
		return "every bucket"
	}
	return "buckets " + strings.Join(buckets, ", ")
}

func listS3Keys(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	listed, err := getS3Keys(ctx, c, projectID)
	if err != nil {
		return nil, err
	}
	keys := make([]listedS3Key, 0, len(listed.Keys))
	for _, key := range listed.Keys {
		shown := key.listedS3Key
		shown.Permissions = s3KeyPermissions{Read: key.Read, Write: key.Write, Delete: key.Delete}
		shown.Buckets = nonNil(shown.Buckets)
		keys = append(keys, shown)
	}
	return map[string]any{"keys": keys, "endpoint": listed.Endpoint, "region": listed.Region, "note": s3KeysNote}, nil
}

func revokeS3Key(ctx context.Context, c *call, in s3KeyArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	if !s3KeyIDPattern.MatchString(in.KeyID) {
		return nil, errors.New("key_id must be a key's id (s3k_ and 32 hex characters), as list_s3_keys shows it")
	}
	preview, err := c.guard(ctx, destruction{
		tool: "revoke_s3_key", target: confirmTarget{projectID, in.KeyID}, token: in.ConfirmationToken,
		describe: func(ctx context.Context) (string, error) {
			listed, err := getS3Keys(ctx, c, projectID)
			if err != nil {
				return "", err
			}
			for _, key := range listed.Keys {
				if key.ID == in.KeyID {
					return fmt.Sprintf("the S3 access key %q (%s, %s): every client using it is refused from the next request, and a revoked key cannot be restored",
						key.Name, key.AccessKeyID, bucketScope(key.Buckets)), nil
				}
			}
			return "", fmt.Errorf("no S3 key %s in this project: list_s3_keys shows them", in.KeyID)
		},
	})
	if preview != nil || err != nil {
		return preview, err
	}
	if err := c.send(ctx, http.MethodDelete, projectsAPI+projectID+"/storage/s3-keys/"+in.KeyID, nil, nil, nil); err != nil {
		return nil, err
	}
	return map[string]any{"revoked": in.KeyID}, nil
}
