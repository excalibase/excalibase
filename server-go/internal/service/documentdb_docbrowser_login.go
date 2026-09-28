package service

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// DocBrowserLogin is the Mongo user Studio's document browser logs in as
// (EXC-410). It is a member of the Mongo users group, so pg_hba admits it on
// the gateway's loopback only; the platform's Postgres roles log in by
// certificate and are refused there. The "excalibase" prefix is reserved, so
// no customer can create, rotate or delete it.
const DocBrowserLogin = config.DocumentDBBrowserLogin

const docBrowserStep = "create document browser login"

// docBrowserVaultPath files the login beside the platform's own credentials,
// outside the prefix the project's Mongo user listing reads.
func docBrowserVaultPath(projectID string) string {
	return vaultCredentialPath(projectID, "docbrowser")
}

// createDocBrowserLogin creates the login with a fresh password, maps it for
// the extension's socket and files it. It reuses the Mongo user statements
// (EXC-427); a restored cluster's copy was dropped with the source's users.
func (s *ProvisioningService) createDocBrowserLogin(ctx context.Context, inst *domain.DatabaseInstance, primary string, pc *provisioner.ProvisionContext) error {
	pc.SetStep(docBrowserStep)
	password := generatePassword(mongoUserPasswordLength)
	verifier, err := newScramVerifier(password)
	if err != nil {
		return pc.Fail(err)
	}
	if err := s.execMongoUserSQL(ctx, inst, primary, createMongoUserSQL(DocBrowserLogin, verifier, MongoRoleReadWrite)); err != nil {
		return pc.Fail(fmt.Errorf("create the document browser login in %s: %w", inst.ProjectID, err))
	}
	if err := s.mapMongoUser(ctx, inst, primary, DocBrowserLogin); err != nil {
		s.undoMongoUser(ctx, inst, primary, DocBrowserLogin)
		return pc.Fail(fmt.Errorf("map the document browser login in %s: %w", inst.ProjectID, err))
	}
	path := docBrowserVaultPath(inst.ProjectID)
	if err := s.vault.Put(path, map[string]string{"username": DocBrowserLogin, "password": password}); err != nil {
		s.undoMongoUser(ctx, inst, primary, DocBrowserLogin)
		return pc.Fail(fmt.Errorf("file the document browser login of %s: %w", inst.ProjectID, err))
	}
	pc.RegisterCleanup("delete vault "+path, func(context.Context) error {
		return s.vault.Delete(path)
	})
	return nil
}
