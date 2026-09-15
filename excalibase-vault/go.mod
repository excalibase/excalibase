module github.com/excalibase/excalibase-vault

go 1.25.11

require (
	github.com/excalibase/provisioning-poc v0.0.0
	github.com/go-chi/chi/v5 v5.2.5
	github.com/lib/pq v1.10.9
)

require (
	github.com/aws/aws-sdk-go-v2 v1.42.1 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.32.20 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.19.19 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.25 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.30 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.30 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.26 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.10 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.25 // indirect
	github.com/aws/aws-sdk-go-v2/service/kms v1.54.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.1.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.30.19 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.36.2 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.42.3 // indirect
	github.com/aws/smithy-go v1.27.3 // indirect
	github.com/hashicorp/vault v1.15.4 // indirect
)

replace github.com/excalibase/provisioning-poc => ../server-go
