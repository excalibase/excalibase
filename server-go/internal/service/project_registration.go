package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/tenantcert"
)

// Role names the platform creates in every project database. They are also
// the vault path suffixes credentials are filed under.
const (
	roleAdmin     = "admin"
	roleAuthAdmin = "auth_admin"
	roleApp       = "excalibase_app"
	roleWatcher   = "cdc_watcher"
)

// ErrProjectRegistrationInvalid is returned when a caller asks to register a
// project without the minimum identity (a project id).
var ErrProjectRegistrationInvalid = errors.New("project registration: project id is required")

// ErrProjectCredentialsExist is returned when a project being registered
// already has credentials filed in vault under its id.
var ErrProjectCredentialsExist = errors.New("project registration: credentials already exist for this project id")

// ProjectRegistrar turns a live database into a project the API can serve.
// Implemented by *ProvisioningService; the backup adapters depend on this
// narrow surface so a restore finishes exactly the way a provision does.
type ProjectRegistrar interface {
	RegisterProject(ctx context.Context, inst *domain.DatabaseInstance, opts RegistrationOptions) error
}

// ProjectEventPublisher is notified once a project becomes usable, so the
// data plane drops any cache it holds for that project id. *PolicyChangePublisher
// satisfies it.
type ProjectEventPublisher interface {
	PublishPolicyChange(ctx context.Context, evt domain.PolicyChangeEvent)
}

// RegistrationOptions tunes how a project's credentials are established.
type RegistrationOptions struct {
	// ResetRolePasswords re-sets the platform roles' passwords after creating
	// them. Required for a restored cluster, whose roles arrived from the
	// source project with the source's passwords.
	ResetRolePasswords bool
	// ResetAdminPassword additionally re-sets the admin/owner role's password
	// to inst.Password. Docker restores need it: the seeded data directory
	// keeps the source's superuser password and ignores the container's
	// POSTGRES_PASSWORD env.
	ResetAdminPassword bool
	// Context threads an in-flight provision's rollback registry. When nil,
	// RegisterProject owns a private one and rolls it back on failure.
	Context *provisioner.ProvisionContext
	// RowAlreadyCreated says the project row was created earlier in this
	// operation — the provision pipeline inserts it up front so its stages
	// are observable. Registration then updates that row instead of
	// creating it. A restore leaves this false, so a target id that is
	// already registered is refused instead of repointing its owner's
	// project (EXC-415).
	RowAlreadyCreated bool
	// Unverified persists the project as RESTORING instead of ACTIVE. The
	// caller flips it once it has proved the database answers a query with
	// the credentials this registration filed, so a restore that recovered
	// nothing never surfaces as a usable project (EXC-401).
	Unverified bool
	// AddingDatabase registers a database added to a project created without
	// one: once the row is ACTIVE again the project is recorded as having its
	// database, and a failure to record that fails the registration (EXC-426).
	AddingDatabase bool
}

// SetActivityRecorder wires the last-seen writer. Optional: without it a newly
// registered project simply has no activity marker until its first API call.
func (s *ProvisioningService) SetActivityRecorder(r *ActivityRecorder) {
	s.activity = r
}

// SetProjectEventPublisher wires the NATS publisher used to announce a newly
// registered project. Optional; nil means no announcement.
func (s *ProvisioningService) SetProjectEventPublisher(p ProjectEventPublisher) {
	s.projectEvents = p
}

// RegisterProject is the single path that makes a project usable, shared by
// provisioning and by restore. In order: engine roles + vault credentials,
// the ACTIVE instance row, the project-created event and the activity marker.
//
// Nothing is persisted until credentials are in place, so a failure leaves no
// half-project: the caller sees an error, the row is absent, and the database
// (namespace or container) stays up for inspection.
func (s *ProvisioningService) RegisterProject(ctx context.Context, inst *domain.DatabaseInstance, opts RegistrationOptions) error {
	if inst == nil || inst.ProjectID == "" {
		return ErrProjectRegistrationInvalid
	}
	pc, owned := opts.Context, false
	if pc == nil {
		pc = provisioner.NewProvisionContext(func(domain.ProvisioningStage) {}, func(string) {})
		owned = true
	}
	if err := s.setupProjectCredentials(ctx, inst, opts, pc); err != nil {
		return rollbackIfOwned(ctx, pc, owned, err)
	}
	// A DocumentDB project's extension is created before the project is
	// recorded as anything a caller may use. A failure here fails the whole
	// registration, so a project is never reported ACTIVE with DocumentDB on
	// a database that does not have it (EXC-409).
	if err := s.enableDocumentDB(ctx, inst, pc); err != nil {
		return rollbackIfOwned(ctx, pc, owned, err)
	}
	if opts.Unverified {
		markProjectRestoring(inst)
	} else {
		markProjectActive(inst)
	}
	if err := s.persistProjectRow(ctx, inst, opts); err != nil {
		return rollbackIfOwned(ctx, pc, owned, fmt.Errorf("persist project row: %w", err))
	}
	if err := s.markDatabaseAdded(inst, opts); err != nil {
		return rollbackIfOwned(ctx, pc, owned, err)
	}
	s.announceProject(ctx, inst)
	return nil
}

// persistProjectRow writes the registered project. Creating is the default so
// an id that is already registered is a conflict; only an operation that
// created the row itself earlier may update it.
//
// A create here is a new project taking one of its organisation's slots — a
// restore is a creation as much as a provision is — so it goes through the
// same limited insert, at the organisation's current plan. An update does
// not: the slot was taken when the provision pipeline inserted the row.
func (s *ProvisioningService) persistProjectRow(ctx context.Context, inst *domain.DatabaseInstance, opts RegistrationOptions) error {
	if opts.RowAlreadyCreated {
		return s.store.Update(inst)
	}
	tier, err := s.orgTier(ctx, inst.OrgID)
	if err != nil {
		return err
	}
	inst.Tier = tier
	return s.createProjectRow(ctx, inst, tier)
}

// markDatabaseAdded records that a project created without a database now
// has one. It runs after the row is ACTIVE, so the database routes open only
// once there is a registered database behind them.
func (s *ProvisioningService) markDatabaseAdded(inst *domain.DatabaseInstance, opts RegistrationOptions) error {
	if !opts.AddingDatabase {
		return nil
	}
	databases, ok := s.store.(storage.ProjectDatabaseStore)
	if !ok {
		return errors.New("this platform's store cannot add a database to a project")
	}
	if err := databases.MarkDatabaseAdded(inst.ProjectID); err != nil {
		return fmt.Errorf("record the added database: %w", err)
	}
	inst.NoDatabase = false
	return nil
}

// rollbackIfOwned runs the compensations RegisterProject registered itself.
// When the context came from a caller (a provision in flight), that caller
// runs the rollback as part of its own failure handling.
func rollbackIfOwned(ctx context.Context, pc *provisioner.ProvisionContext, owned bool, err error) error {
	if owned {
		pc.Rollback(ctx)
	}
	return err
}

// markProjectRestoring stamps the not-yet-proved state a restore registers
// its target in. The row exists (so DELETE and the busy checks can see it)
// but no caller may treat it as a working project.
func markProjectRestoring(inst *domain.DatabaseInstance) {
	markProjectActive(inst)
	inst.Status = string(domain.StatusRestoring)
	inst.CurrentStage = domain.StatusRestoring
	// DELETE is an abandoned restore's only way out; protection starts at ACTIVE.
	inst.DeletionProtection = nil
}

// markProjectActive stamps the terminal provisioning state onto the row.
func markProjectActive(inst *domain.DatabaseInstance) {
	inst.Status = "ACTIVE"
	inst.CurrentStage = domain.StageCompleted
	inst.CurrentStep = ""
	if inst.DeletionProtection == nil {
		inst.DeletionProtection = boolPtr(true)
	}
	if inst.PoolerEnabled == nil {
		inst.PoolerEnabled = boolPtr(false)
	}
	now := &domain.FlexTime{Time: time.Now()}
	if inst.CreatedAt == nil {
		inst.CreatedAt = now
	}
	inst.UpdatedAt = now
	inst.LastHealthCheck = now
}

// setupProjectCredentials creates the platform roles and files their
// credentials, the owner's among them, in vault. The vault is the only place
// the owner credential is kept, so without one registration is refused.
// Role creation is skipped when the platform has no way to execute SQL in the
// project's database.
func (s *ProvisioningService) setupProjectCredentials(ctx context.Context, inst *domain.DatabaseInstance, opts RegistrationOptions, pc *provisioner.ProvisionContext) error {
	if s.vault == nil || s.vault.Sealed() {
		return pc.Fail(fmt.Errorf("%w: the vault is not available", ErrOwnerCredentialUnavailable))
	}
	port := 5432
	if inst.Port != nil {
		port = *inst.Port
	}
	spec := projectRoleSpec{
		projectID:      inst.ProjectID,
		namespace:      inst.Namespace,
		host:           inst.Host,
		port:           port,
		databaseName:   inst.DatabaseName,
		adminUsername:  inst.Username,
		adminPassword:  inst.Password,
		resetPasswords: opts.ResetRolePasswords,
		resetAdmin:     opts.ResetAdminPassword,
	}
	canExecSQL := (s.k8sClient != nil) || (s.dockerClient != nil)
	if !canExecSQL {
		return s.fileOwnerCredential(spec, pc)
	}
	return s.createProjectRoles(ctx, spec, newProjectRoleCredentials(), pc)
}

// projectRoleCredentials holds the generated passwords for the roles
// createProjectRoles creates.
type projectRoleCredentials struct {
	authPassword    string
	appPassword     string
	watcherPassword string
}

func newProjectRoleCredentials() projectRoleCredentials {
	return projectRoleCredentials{
		authPassword:    generatePassword(32),
		appPassword:     generatePassword(32),
		watcherPassword: generatePassword(32),
	}
}

// announceProject publishes the project-created event and seeds the activity
// marker so a freshly registered project is not immediately idle-paused.
func (s *ProvisioningService) announceProject(ctx context.Context, inst *domain.DatabaseInstance) {
	if s.projectEvents != nil {
		s.projectEvents.PublishPolicyChange(ctx, domain.PolicyChangeEvent{
			ProjectID: inst.ProjectID,
			Kind:      "project",
			Op:        "create",
		})
	}
	if s.activity != nil {
		s.activity.Record(ctx, inst.ProjectID, activitySourceFor(inst))
	}
}

func activitySourceFor(inst *domain.DatabaseInstance) domain.ActivitySource {
	if inst.RestoredFromProjectID != "" {
		return domain.ActivitySourceBackup
	}
	return domain.ActivitySourceAPI
}

// projectRoleSpec describes the database the platform roles are created in.
// Built from a provision result or from a restored cluster.
type projectRoleSpec struct {
	projectID      string
	namespace      string
	host           string
	port           int
	databaseName   string
	adminUsername  string
	adminPassword  string
	resetPasswords bool
	resetAdmin     bool
}

// createProjectRoles executes the role SQL in the project's database and files
// every credential in vault, registering a compensation per vault write so a
// later failure cannot strand secrets.
func (s *ProvisioningService) createProjectRoles(ctx context.Context, spec projectRoleSpec, creds projectRoleCredentials, pc *provisioner.ProvisionContext) error {
	pc.SetStage(domain.StageRoleCreation)

	if err := s.fileOwnerCredential(spec, pc); err != nil {
		return err
	}

	pc.SetStep("exec CREATE ROLE in database")
	if err := s.execProjectRoleSQL(ctx, spec, creds); err != nil {
		return pc.Fail(err)
	}

	pc.SetStep("issue platform role certificates")
	certificates, err := s.issueRoleCertificates(ctx, spec.namespace, spec.projectID)
	if err != nil {
		return pc.Fail(err)
	}

	passwords := map[string]string{
		roleAuthAdmin: creds.authPassword,
		roleApp:       creds.appPassword,
		roleWatcher:   creds.watcherPassword,
	}
	for _, role := range []string{roleAuthAdmin, roleApp, roleWatcher} {
		pc.SetStep("store " + role + " credentials")
		record := specCredentialRecord(spec, role, passwords[role])
		if material, ok := certificates[role]; ok {
			record = material.AddTo(record)
		}
		if err := s.putCredentialRecord(spec.projectID, role, record, pc); err != nil {
			return pc.Fail(err)
		}
	}

	log.Printf("Created project roles for %s and stored in vault", spec.projectID)
	return s.deployWatcher(ctx, spec, pc, certificates[roleWatcher])
}

// fileOwnerCredential files the owner credential in vault under a project id
// that holds no credentials yet.
func (s *ProvisioningService) fileOwnerCredential(spec projectRoleSpec, pc *provisioner.ProvisionContext) error {
	if err := s.assertNoStoredCredentials(spec.projectID); err != nil {
		return pc.Fail(err)
	}
	pc.SetStep("store admin credentials")
	record := specCredentialRecord(spec, spec.adminUsername, spec.adminPassword)
	if err := s.putCredentialRecord(spec.projectID, roleAdmin, record, pc); err != nil {
		return pc.Fail(err)
	}
	return nil
}

// execProjectRoleSQL runs the create-if-absent role SQL, followed by the
// password reset when the cluster arrived with roles already in it.
func (s *ProvisioningService) execProjectRoleSQL(ctx context.Context, spec projectRoleSpec, creds projectRoleCredentials) error {
	roleSQL := BuildProjectRoleSQL(creds.authPassword, creds.appPassword, creds.watcherPassword,
		spec.databaseName, s.publicationName)
	if spec.resetPasswords {
		roleSQL += BuildProjectRoleResetSQL(resetTargets(spec, creds))
	}
	primaryPod := spec.projectID + primaryPodSuffix
	cmd := []string{"psql", "-U", "postgres", "-d", spec.databaseName, "-c", roleSQL}
	return s.execRoleSQL(ctx, spec.namespace, primaryPod, cmd)
}

// resetTargets lists the roles whose password must be forced to the value the
// platform just filed in vault.
func resetTargets(spec projectRoleSpec, creds projectRoleCredentials) []RolePassword {
	targets := []RolePassword{
		{Role: roleAuthAdmin, Password: creds.authPassword},
		{Role: roleApp, Password: creds.appPassword},
		{Role: roleWatcher, Password: creds.watcherPassword},
	}
	if spec.resetAdmin {
		targets = append(targets, RolePassword{Role: spec.adminUsername, Password: spec.adminPassword})
	}
	return targets
}

// assertNoStoredCredentials refuses to register a project whose vault prefix
// already holds credentials. Registration only ever runs for a project being
// brought into existence, so entries under its id belong to a different
// project that happens to share the id — writing there would hand that
// project's clients the new database's credentials (EXC-415).
func (s *ProvisioningService) assertNoStoredCredentials(projectID string) error {
	existing, err := s.vault.List(vaultCredentialPrefix(projectID))
	if err != nil {
		return fmt.Errorf("list existing credentials: %w", err)
	}
	if len(existing) > 0 {
		return ErrProjectCredentialsExist
	}
	return nil
}

// specCredentialRecord is one role's connection details as vault files them.
func specCredentialRecord(spec projectRoleSpec, username, password string) map[string]string {
	return map[string]string{
		"host":     spec.host,
		"port":     strconv.Itoa(spec.port),
		"database": spec.databaseName,
		"username": username,
		"password": password,
	}
}

// putCredentialRecord writes one role's record to vault and registers its
// deletion as a compensation.
func (s *ProvisioningService) putCredentialRecord(projectID, role string, record map[string]string, pc *provisioner.ProvisionContext) error {
	path := vaultCredentialPath(projectID, role)
	if err := s.vault.Put(path, record); err != nil {
		return fmt.Errorf("vault put %s: %w", role, err)
	}
	pc.RegisterCleanup("delete vault "+path, func(context.Context) error {
		return s.vault.Delete(path)
	})
	return nil
}

// deployWatcher starts the per-project CDC watcher now that cdc_watcher
// exists. The chart install itself is best-effort — a project without
// realtime still works — but minting its NATS bus identity is not: a watcher
// started without one is unauthenticated against auth_callout (EXC-324), so
// that failure fails registration instead of deploying a broken watcher.
func (s *ProvisioningService) deployWatcher(ctx context.Context, spec projectRoleSpec, pc *provisioner.ProvisionContext, watcherCert tenantcert.Material) error {
	if s.factory == nil {
		return nil
	}
	pgProv, ok := s.factory.Get(domain.PostgreSQL)
	if !ok {
		return nil
	}
	pg, ok := pgProv.(*provisioner.PostgreSQLProvisioner)
	if !ok {
		return nil
	}
	// Mint the watcher's bus identity before the pod starts. Rotating here
	// means a re-provision (or restore) invalidates the old pod's credential.
	natsUser, natsPass, err := s.natsCreds.MintTenantWatcher(ctx, spec.projectID)
	if err != nil {
		return pc.Fail(fmt.Errorf("mint watcher nats credential: %w", err))
	}
	watcherSpec := provisioner.WatcherSpec{
		Namespace:    spec.namespace,
		ProjectID:    spec.projectID,
		DBName:       spec.databaseName,
		Username:     roleWatcher,
		NatsUser:     natsUser,
		NatsPassword: natsPass,
		ClientCert:   watcherCert,
	}
	if err := pg.DeployWatcher(ctx, watcherSpec); err != nil {
		log.Printf("WARN: watcher deployment for %s: %v", spec.projectID, err)
	}
	return nil
}

// ErrReplicationRestartUnavailable is returned when the platform cannot put
// a resumed project's CDC watcher back. There is no partial answer: a resume
// that silently leaves realtime off is a project that looks healthy and
// publishes nothing.
var ErrReplicationRestartUnavailable = errors.New("replication restart: watcher dependencies not configured")

// RestartReplication reinstalls a project's CDC watcher. A pause removes it
// so its replication session stops holding the database's shutdown open
// (EXC-363); a resume puts it back, and only after the primary is serving —
// the pause service calls this once the provisioner reports the workload up.
func (s *ProvisioningService) RestartReplication(ctx context.Context, inst *domain.DatabaseInstance) error {
	if inst == nil {
		return ErrProjectRegistrationInvalid
	}
	pg, err := s.postgresProvisioner()
	if err != nil {
		return err
	}
	if s.vault == nil || s.natsCreds == nil {
		return ErrReplicationRestartUnavailable
	}
	creds, err := s.vault.Get(fmt.Sprintf("projects/%s/credentials/%s", inst.ProjectID, roleWatcher))
	if err != nil {
		return fmt.Errorf("read %s credentials: %w", roleWatcher, err)
	}
	watcherCert, err := tenantcert.FromRecord(creds)
	if err != nil {
		return fmt.Errorf("%s: %w", roleWatcher, err)
	}
	natsUser, natsPass, err := s.natsCreds.MintTenantWatcher(ctx, inst.ProjectID)
	if err != nil {
		return fmt.Errorf("mint watcher nats credential: %w", err)
	}
	return pg.DeployWatcher(ctx, provisioner.WatcherSpec{
		Namespace:    inst.Namespace,
		ProjectID:    inst.ProjectID,
		DBName:       inst.DatabaseName,
		Username:     roleWatcher,
		NatsUser:     natsUser,
		NatsPassword: natsPass,
		ClientCert:   watcherCert,
	})
}

// postgresProvisioner resolves the CNPG provisioner the watcher chart is
// installed through.
func (s *ProvisioningService) postgresProvisioner() (*provisioner.PostgreSQLProvisioner, error) {
	if s.factory == nil {
		return nil, ErrReplicationRestartUnavailable
	}
	prov, ok := s.factory.Get(domain.PostgreSQL)
	if !ok {
		return nil, ErrReplicationRestartUnavailable
	}
	pg, ok := prov.(*provisioner.PostgreSQLProvisioner)
	if !ok {
		return nil, ErrReplicationRestartUnavailable
	}
	return pg, nil
}
