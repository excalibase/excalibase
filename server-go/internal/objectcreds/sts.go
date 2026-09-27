package objectcreds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

// stsMinTTL is the shortest session AssumeRole grants.
const stsMinTTL = 15 * time.Minute

// stsRoleARN is required by the SDK; MinIO's AssumeRole ignores it and derives
// the session from the signing user narrowed by the inline policy.
const stsRoleARN = "arn:xxx:xxx:xxx:xxxx"

// STSMinter asks an S3-compatible STS endpoint (MinIO) for an AssumeRole
// session whose inline policy confines it to one prefix of one bucket.
type STSMinter struct{}

type policyStatement struct {
	Effect    string                         `json:"Effect"`
	Action    []string                       `json:"Action"`
	Resource  []string                       `json:"Resource"`
	Condition map[string]map[string][]string `json:"Condition,omitempty"`
}

type policyDocument struct {
	Version   string            `json:"Version"`
	Statement []policyStatement `json:"Statement"`
}

// Mint requests a session for scope, signed with the parent key.
func (STSMinter) Mint(ctx context.Context, parent Parent, scope Scope) (Credentials, error) {
	if err := validate(parent, scope, stsMinTTL); err != nil {
		return Credentials{}, err
	}
	if parent.Endpoint == "" {
		return Credentials{}, fmt.Errorf("%w: STS endpoint required", ErrInvalidRequest)
	}
	policy, err := sessionPolicy(parent.Bucket, scope)
	if err != nil {
		return Credentials{}, err
	}
	region := parent.Region
	if region == "" {
		region = "us-east-1"
	}
	client := sts.New(sts.Options{
		Region:       region,
		BaseEndpoint: aws.String(parent.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(parent.AccessKeyID, parent.SecretAccessKey, ""),
	})
	out, err := client.AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn:         aws.String(stsRoleARN),
		RoleSessionName: aws.String("excalibase-backup"),
		DurationSeconds: aws.Int32(int32(scope.TTL / time.Second)),
		Policy:          aws.String(policy),
	})
	if err != nil {
		return Credentials{}, fmt.Errorf("objectcreds: assume role at %s: %s", parent.Endpoint, stsFailure(err))
	}
	if out.Credentials == nil || out.Credentials.Expiration == nil {
		return Credentials{}, errors.New("objectcreds: assume role answered without credentials")
	}
	return Credentials{
		AccessKeyID:     aws.ToString(out.Credentials.AccessKeyId),
		SecretAccessKey: aws.ToString(out.Credentials.SecretAccessKey),
		SessionToken:    aws.ToString(out.Credentials.SessionToken),
		ExpiresAt:       out.Credentials.Expiration.UTC(),
	}, nil
}

// stsFailure names why the endpoint refused, without echoing the request.
func stsFailure(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() + ": " + apiErr.ErrorMessage()
	}
	return "request failed"
}

func sessionPolicy(bucket string, scope Scope) (string, error) {
	objectActions := []string{"s3:GetObject"}
	if scope.Access == ReadWrite {
		objectActions = []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"}
	}
	doc := policyDocument{
		Version: "2012-10-17",
		Statement: []policyStatement{
			{Effect: "Allow", Action: objectActions, Resource: []string{"arn:aws:s3:::" + bucket + "/" + scope.Prefix + "*"}},
			{
				Effect: "Allow", Action: []string{"s3:ListBucket"}, Resource: []string{"arn:aws:s3:::" + bucket},
				Condition: map[string]map[string][]string{"StringLike": {"s3:prefix": {scope.Prefix + "*"}}},
			},
		},
	}
	raw, err := json.Marshal(doc)
	return string(raw), err
}
