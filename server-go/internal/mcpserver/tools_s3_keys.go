package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"
)

// S3 access keys (EXC-630): a coding tool may see which keys a project holds
// and revoke one. Issuing a key mints a credential, which ADR 0037 keeps in
// Studio, so there is no create tool.

var s3KeyIDPattern = regexp.MustCompile(`^s3k_[0-9a-f]{32}$`)

type s3KeyArgs struct {
	projectArg
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
		tool("list_s3_keys", "The project's S3 access keys: names, access key ids, buckets, permissions and last use, "+
			"with the endpoint and region S3 clients use. Never a secret.", readTool, listS3Keys),
		tool("revoke_s3_key", "Revoke one S3 access key at once; every client using it is refused from the next request.", writeTool, revokeS3Key),
	}
}

func listS3Keys(ctx context.Context, c *call, in projectArg) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	var listed struct {
		Keys     []storedS3Key `json:"keys"`
		Endpoint string        `json:"endpoint"`
		Region   string        `json:"region"`
	}
	if err := c.get(ctx, projectsAPI+projectID+"/storage/s3-keys", nil, &listed); err != nil {
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
	if err := c.send(ctx, http.MethodDelete, projectsAPI+projectID+"/storage/s3-keys/"+in.KeyID, nil, nil, nil); err != nil {
		return nil, err
	}
	return map[string]any{"revoked": in.KeyID}, nil
}
