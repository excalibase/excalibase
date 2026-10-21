package main

import (
	"context"
	"errors"
	"log"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/handler"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/s3keys"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

// s3KeyStanding answers by the deploy route's own checks: an active account
// with project access on the Developer rung (EXC-630 decision 2).
type s3KeyStanding struct{ members custommw.DeployTokenStanding }

func (s s3KeyStanding) CanUse(ctx context.Context, userID, projectID string) (bool, error) {
	standing, err := s.members.Standing(ctx, userID, projectID)
	return standing.CanDeploy, err
}

type s3KeyBuckets struct{ storage *storagesvc.Service }

func (b s3KeyBuckets) BucketExists(ctx context.Context, projectID, name string) (bool, error) {
	_, err := b.storage.Bucket(ctx, projectID, name)
	if errors.Is(err, storagesvc.ErrBucketNotFound) {
		return false, nil
	}
	return err == nil, err
}

// newS3Keys builds the S3 access key service (EXC-630). It needs the object
// store and the vault; without either there is nothing to issue keys for.
func newS3Keys(sqlStore storage.PlatformStore, instances storage.InstanceStore, vc vaultclient.VaultClient,
	storageSvc *storagesvc.Service) *s3keys.Service {
	if storageSvc == nil || vc == nil {
		return nil
	}
	standing := s3KeyStanding{members: custommw.DeployTokenStanding{Users: sqlStore, Instances: instances, Orgs: sqlStore}}
	return s3keys.NewService(s3keys.NewPostgresStore(sqlStore.DB()), s3keys.NewVaultSecrets(vc), standing, s3KeyBuckets{storage: storageSvc})
}

// wireS3Keys revokes a deleted project's keys and serves them to Studio and MCP.
func wireS3Keys(cfg config.AppConfig, keys *s3keys.Service, sqlStore storage.PlatformStore, provSvc *service.ProvisioningService) *handler.S3KeysHandler {
	if keys == nil {
		return nil
	}
	provSvc.SetS3KeyPurger(keys)
	h := handler.NewS3KeysHandler(keys, cfg.S3PublicURL)
	h.SetAuditLog(sqlStore)
	if cfg.S3PublicURL == "" {
		log.Println("WARN: S3_PUBLIC_URL is not set; Studio cannot tell S3 clients where the gateway is")
	}
	return h
}
