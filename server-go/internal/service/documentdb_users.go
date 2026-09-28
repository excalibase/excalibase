package service

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Mongo users a DocumentDB project creates for itself (EXC-427).
//
// The platform creates them; the project's own roles cannot (no CREATEROLE,
// no ADMIN OPTION), so the platform's record is the full list. Each user is a
// LOGIN role in the project's cluster, a member of the Mongo users group that
// pg_hba trusts only on loopback for the gateway and refuses everywhere else,
// plus one DocumentDB role: documentdb_admin_role for read-write (upstream's
// readWriteAnyDatabase+clusterAdmin, the access the owner credential has) or
// documentdb_readonly_role with pg_read_all_data for read-only, exactly as
// upstream's createUser grants them.
//
// The password is generated here, returned once, and filed in vault; the
// database receives only its SCRAM verifier, over exec stdin.

// MongoUserRole is one of the two roles the owner agreed to offer.
type MongoUserRole string

const (
	MongoRoleReadWrite MongoUserRole = "readWrite"
	MongoRoleRead      MongoUserRole = "read"
)

// MaxMongoUsersPerProject caps a project's own users (owner decision, in line
// with hosted Mongo services). The gateway opens a user's connection pool only
// while that user is connected, against the tier's max_connections.
const MaxMongoUsersPerProject = 100

// OperationMongoUsers holds the project lease so the cap and the name check
// see every concurrent change.
const OperationMongoUsers ProjectOperation = "Mongo user change"

const (
	mongoUserPasswordLength = 32
	scramIterations         = 4096
	scramSaltLength         = 16
	// terminateWaitMillis bounds how long a delete waits for the user's sessions to end.
	terminateWaitMillis = 5000
)

var (
	ErrInvalidMongoUsername  = errors.New("invalid Mongo user name: use 3-63 lowercase letters, digits or underscores, starting with a letter, and not a reserved name")
	ErrInvalidMongoUserRole  = errors.New(`invalid Mongo user role: use "readWrite" or "read"`)
	ErrNotDocumentDBProject  = errors.New("only projects created with DocumentDB have Mongo users")
	ErrMongoUserExists       = errors.New("a user with this name already exists in the project")
	ErrMongoUserNotFound     = errors.New("no such Mongo user in this project")
	ErrMongoUserLimit        = fmt.Errorf("a project can have at most %d Mongo users", MaxMongoUsersPerProject)
	ErrMongoUsersUnavailable = errors.New("the platform needs its Kubernetes client and an unsealed vault to manage Mongo users")
)

var mongoUsernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,62}$`)

// reservedMongoUsernames are the platform's own roles and names a client or
// operator would read as privileged.
var reservedMongoUsernames = []string{
	"postgres", "app", roleApp, roleAuthAdmin, roleWatcher, roleAdmin, "root",
	config.DocumentDBGatewayRole, "streaming_replica", "public", "none",
	"anon", "authenticated", "service_role", "authenticator",
	"current_user", "current_role", "session_user", "user",
}

var reservedMongoUsernamePrefixes = []string{"pg_", "documentdb", "excalibase", "cnpg"}

// MongoUser is what a listing shows: never the password.
type MongoUser struct {
	Username  string        `json:"username"`
	Role      MongoUserRole `json:"role"`
	CreatedAt string        `json:"createdAt"`
}

// MongoUserCredential is returned by create and rotate, and nowhere else.
type MongoUserCredential struct {
	Username string        `json:"username"`
	Role     MongoUserRole `json:"role"`
	Password string        `json:"password"`
}

// ValidateMongoUsername refuses anything but a plain lowercase identifier
// that is not reserved. Statements still quote it; this keeps names readable.
func ValidateMongoUsername(name string) error {
	if !mongoUsernamePattern.MatchString(name) || slices.Contains(reservedMongoUsernames, name) {
		return ErrInvalidMongoUsername
	}
	for _, prefix := range reservedMongoUsernamePrefixes {
		if strings.HasPrefix(name, prefix) {
			return ErrInvalidMongoUsername
		}
	}
	return nil
}

func (r MongoUserRole) Validate() error {
	if r != MongoRoleReadWrite && r != MongoRoleRead {
		return ErrInvalidMongoUserRole
	}
	return nil
}

// documentDBGrants are the roles a user of this kind is granted besides the group.
func (r MongoUserRole) documentDBGrants() []string {
	if r == MongoRoleReadWrite {
		return []string{config.DocumentDBExtension + "_admin_role"}
	}
	return []string{config.DocumentDBExtension + "_readonly_role", "pg_read_all_data"}
}

// ListMongoUsers returns the project's own Mongo users, sorted by name.
func (s *ProvisioningService) ListMongoUsers(_ context.Context, projectID string) ([]MongoUser, error) {
	if err := s.mongoUsersAvailable(); err != nil {
		return nil, err
	}
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return nil, err
	}
	if !inst.DocumentDB {
		return nil, ErrNotDocumentDBProject
	}
	return s.filedMongoUsers(projectID)
}

// CreateMongoUser creates the role, files its credential and returns the
// password, which is never shown again.
func (s *ProvisioningService) CreateMongoUser(ctx context.Context, projectID, username string, role MongoUserRole) (*MongoUserCredential, error) {
	if err := ValidateMongoUsername(username); err != nil {
		return nil, err
	}
	if err := role.Validate(); err != nil {
		return nil, err
	}
	inst, release, err := s.holdMongoUsers(ctx, projectID)
	if err != nil {
		return nil, err
	}
	defer release()

	if err := s.admitNewMongoUser(inst, username); err != nil {
		return nil, err
	}
	primary, err := s.currentPrimary(ctx, inst)
	if err != nil {
		return nil, err
	}
	password := generatePassword(mongoUserPasswordLength)
	verifier, err := newScramVerifier(password)
	if err != nil {
		return nil, err
	}
	if err := s.execMongoUserSQL(ctx, inst, primary, createMongoUserSQL(username, verifier, role)); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return nil, ErrMongoUserExists
		}
		return nil, fmt.Errorf("create Mongo user %s in %s: %w", username, projectID, err)
	}
	if err := s.mapMongoUser(ctx, inst, primary, username); err != nil {
		s.undoMongoUser(ctx, inst, primary, username)
		return nil, fmt.Errorf("map Mongo user %s in %s: %w", username, projectID, err)
	}
	record := map[string]string{
		"username": username, "role": string(role), "password": password,
		"createdAt": time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.vault.Put(mongoUserVaultPath(projectID, username), record); err != nil {
		// Unrecorded, the user would be one nobody could list, rotate or delete.
		s.undoMongoUser(ctx, inst, primary, username)
		return nil, fmt.Errorf("record Mongo user %s in %s: %w", username, projectID, err)
	}
	log.Printf("Mongo user %s (%s) created in %s", username, role, projectID)
	return &MongoUserCredential{Username: username, Role: role, Password: password}, nil
}

// RotateMongoUser gives the user a new password and returns it once. Logins
// with the old one fail from then on; a Mongo connection already
// authenticated keeps working until it disconnects, as in Postgres.
func (s *ProvisioningService) RotateMongoUser(ctx context.Context, projectID, username string) (*MongoUserCredential, error) {
	if err := ValidateMongoUsername(username); err != nil {
		return nil, ErrMongoUserNotFound
	}
	inst, release, err := s.holdMongoUsers(ctx, projectID)
	if err != nil {
		return nil, err
	}
	defer release()

	record, err := s.filedMongoUser(projectID, username)
	if err != nil {
		return nil, err
	}
	primary, err := s.currentPrimary(ctx, inst)
	if err != nil {
		return nil, err
	}
	password := generatePassword(mongoUserPasswordLength)
	verifier, err := newScramVerifier(password)
	if err != nil {
		return nil, err
	}
	if err := s.execMongoUserSQL(ctx, inst, primary, rotateMongoUserSQL(username, verifier)); err != nil {
		return nil, fmt.Errorf("rotate Mongo user %s in %s: %w", username, projectID, err)
	}
	updated := map[string]string{
		"username": username, "role": record["role"], "password": password, "createdAt": record["createdAt"],
	}
	if err := s.vault.Put(mongoUserVaultPath(projectID, username), updated); err != nil {
		return nil, fmt.Errorf("record rotated Mongo user %s in %s (rotate again): %w", username, projectID, err)
	}
	log.Printf("Mongo user %s rotated in %s", username, projectID)
	return &MongoUserCredential{Username: username, Role: MongoUserRole(record["role"]), Password: password}, nil
}

// DeleteMongoUser ends the user's sessions, drops the role, removes its peer
// line and forgets it. The vault record goes last: while it exists the same
// call can be retried, a role or line without a record would be invisible.
func (s *ProvisioningService) DeleteMongoUser(ctx context.Context, projectID, username string) error {
	if err := ValidateMongoUsername(username); err != nil {
		return ErrMongoUserNotFound
	}
	inst, release, err := s.holdMongoUsers(ctx, projectID)
	if err != nil {
		return err
	}
	defer release()

	if _, err := s.filedMongoUser(projectID, username); err != nil {
		return err
	}
	primary, err := s.currentPrimary(ctx, inst)
	if err != nil {
		return err
	}
	if err := s.execMongoUserSQL(ctx, inst, primary, dropMongoUserSQL(username)); err != nil {
		return fmt.Errorf("drop Mongo user %s in %s: %w", username, projectID, err)
	}
	if err := s.setMongoUserPeerLine(ctx, inst, username, false); err != nil {
		return fmt.Errorf("unmap Mongo user %s in %s: %w", username, projectID, err)
	}
	if err := s.vault.Delete(mongoUserVaultPath(projectID, username)); err != nil {
		return fmt.Errorf("forget Mongo user %s in %s: %w", username, projectID, err)
	}
	log.Printf("Mongo user %s deleted from %s", username, projectID)
	return nil
}

func (s *ProvisioningService) mongoUsersAvailable() error {
	if s.k8sClient == nil || s.vault == nil || s.vault.Sealed() {
		return ErrMongoUsersUnavailable
	}
	return nil
}

// holdMongoUsers takes the project lease for a change to its Mongo users.
func (s *ProvisioningService) holdMongoUsers(ctx context.Context, projectID string) (*domain.DatabaseInstance, func(), error) {
	if err := s.mongoUsersAvailable(); err != nil {
		return nil, nil, err
	}
	inst, release, err := s.holdProject(ctx, projectID, OperationMongoUsers, requireActive)
	if err != nil {
		return nil, nil, err
	}
	if !inst.DocumentDB {
		release()
		return nil, nil, ErrNotDocumentDBProject
	}
	return inst, release, nil
}

func (s *ProvisioningService) admitNewMongoUser(inst *domain.DatabaseInstance, username string) error {
	if username == inst.Username {
		return ErrMongoUserExists
	}
	existing, err := s.filedMongoUsers(inst.ProjectID)
	if err != nil {
		return err
	}
	for _, user := range existing {
		if user.Username == username {
			return ErrMongoUserExists
		}
	}
	if len(existing) >= MaxMongoUsersPerProject {
		return ErrMongoUserLimit
	}
	return nil
}

func (s *ProvisioningService) filedMongoUsers(projectID string) ([]MongoUser, error) {
	prefix := mongoUserVaultPrefix(projectID)
	paths, err := s.vault.List(prefix)
	if err != nil {
		return nil, fmt.Errorf("list Mongo users of %s: %w", projectID, err)
	}
	users := make([]MongoUser, 0, len(paths))
	for _, path := range paths {
		name, ok := strings.CutPrefix(path, prefix)
		if !ok || name == "" || strings.Contains(name, "/") {
			continue
		}
		record, err := s.vault.Get(path)
		if err != nil {
			return nil, fmt.Errorf("read Mongo user %s of %s: %w", name, projectID, err)
		}
		users = append(users, MongoUser{Username: name, Role: MongoUserRole(record["role"]), CreatedAt: record["createdAt"]})
	}
	slices.SortFunc(users, func(a, b MongoUser) int { return strings.Compare(a.Username, b.Username) })
	return users, nil
}

// filedMongoUser reads one user's record; presence is decided by List so an
// unreadable vault fails instead of reading as "not found".
func (s *ProvisioningService) filedMongoUser(projectID, username string) (map[string]string, error) {
	paths, err := s.vault.List(mongoUserVaultPrefix(projectID))
	if err != nil {
		return nil, fmt.Errorf("list Mongo users of %s: %w", projectID, err)
	}
	path := mongoUserVaultPath(projectID, username)
	if !slices.Contains(paths, path) {
		return nil, ErrMongoUserNotFound
	}
	record, err := s.vault.Get(path)
	if err != nil {
		return nil, fmt.Errorf("read Mongo user %s of %s: %w", username, projectID, err)
	}
	return record, nil
}

// currentPrimary is the pod CNPG reports as primary; after a switchover it is
// not instance 1, and a statement run on a replica would be refused.
func (s *ProvisioningService) currentPrimary(ctx context.Context, inst *domain.DatabaseInstance) (string, error) {
	cluster, err := s.k8sClient.GetCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, inst.ProjectID+postgresClusterSuffix)
	if err != nil {
		return "", fmt.Errorf("read cluster of %s: %w", inst.ProjectID, err)
	}
	primary, _, _ := unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	if primary == "" {
		return "", fmt.Errorf("cluster of %s reports no primary", inst.ProjectID)
	}
	return primary, nil
}

// execMongoUserSQL streams the statement on stdin, never as an exec argument:
// arguments land in the exec URL, which the API server audits.
func (s *ProvisioningService) execMongoUserSQL(ctx context.Context, inst *domain.DatabaseInstance, primary, statement string) error {
	_, err := s.k8sClient.ExecInPodStdin(ctx, inst.Namespace, primary, "postgres",
		[]string{"psql", "-U", "postgres", "-d", config.DocumentDBDatabase, "-q", "-v", "ON_ERROR_STOP=1", "-f", "-"},
		statement+"\n")
	return err
}

func mongoUserVaultPrefix(projectID string) string {
	return vaultProjectPrefix(projectID) + "mongo-users/"
}

func mongoUserVaultPath(projectID, username string) string {
	return mongoUserVaultPrefix(projectID) + username
}

// createMongoUserSQL creates the role with a pre-hashed password and grants
// its memberships. Names and values reach Postgres as hex and are quoted by
// format(); the role carries no attribute beyond LOGIN.
func createMongoUserSQL(username, verifier string, role MongoUserRole) string {
	var b strings.Builder
	b.WriteString("IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = n) THEN RAISE EXCEPTION 'role already exists'; END IF; ")
	b.WriteString("EXECUTE format('CREATE ROLE %I WITH LOGIN PASSWORD %L', n, " + sqlTextLiteral(verifier) + "); ")
	for _, grant := range append([]string{config.DocumentDBMongoUsersGroup}, role.documentDBGrants()...) {
		b.WriteString("EXECUTE format('GRANT %I TO %I', '" + grant + "', n); ")
	}
	return mongoUserBlock(username, b.String())
}

// mongoUserBlock runs body with n bound to the user's name.
func mongoUserBlock(username, body string) string {
	return "DO $$ DECLARE n text := " + sqlTextLiteral(username) + "; BEGIN " + body + " END $$;"
}

// isMongoUserSQL is true only for a direct member of the Mongo users group
// that is not a superuser, so these statements can never touch another role.
const isMongoUserSQL = "EXISTS (SELECT 1 FROM pg_auth_members m JOIN pg_roles u ON u.oid = m.member " +
	"JOIN pg_roles g ON g.oid = m.roleid WHERE u.rolname = n AND NOT u.rolsuper AND g.rolname = '" +
	config.DocumentDBMongoUsersGroup + "')"

const notAMongoUserGuard = "IF NOT " + isMongoUserSQL + " THEN RAISE EXCEPTION 'not a Mongo user'; END IF; "

func rotateMongoUserSQL(username, verifier string) string {
	return mongoUserBlock(username, notAMongoUserGuard+
		"EXECUTE format('ALTER ROLE %I WITH PASSWORD %L', n, "+sqlTextLiteral(verifier)+");")
}

// dropMongoUserSQL drops the user if it exists. Anything it owns goes to the
// DocumentDB admin role first, so no collection is lost with it.
func dropMongoUserSQL(username string) string {
	return mongoUserBlock(username, "IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = n) THEN RETURN; END IF; "+
		notAMongoUserGuard+dropMongoUserBody)
}

// dropMongoUserBody ends role n's sessions and drops it.
var dropMongoUserBody = fmt.Sprintf("PERFORM pg_terminate_backend(pid, %d) FROM pg_stat_activity WHERE usename = n; "+
	"EXECUTE format('REASSIGN OWNED BY %%I TO %s_admin_role', n); "+
	"EXECUTE format('DROP OWNED BY %%I', n); "+
	"EXECUTE format('DROP ROLE %%I', n);", terminateWaitMillis, config.DocumentDBExtension)

// dropAllMongoUsersSQL removes every Mongo user a cluster holds. Registration
// runs it: a restored cluster arrives with the source project's users and
// their passwords, and a new project starts with none of its own.
func dropAllMongoUsersSQL() string {
	return "DO $$ DECLARE n text; BEGIN FOR n IN SELECT u.rolname FROM pg_auth_members m " +
		"JOIN pg_roles u ON u.oid = m.member JOIN pg_roles g ON g.oid = m.roleid " +
		"WHERE g.rolname = '" + config.DocumentDBMongoUsersGroup + "' AND NOT u.rolsuper LOOP " +
		dropMongoUserBody + " END LOOP; END $$"
}

// newScramVerifier salts with crypto/rand, which does not fail (it crashes
// the program rather than return an error since Go 1.24).
func newScramVerifier(password string) (string, error) {
	salt := make([]byte, scramSaltLength)
	rand.Read(salt)
	return scramSHA256Verifier(password, salt)
}

// scramSHA256Verifier is the value Postgres stores for a SCRAM-SHA-256
// password (RFC 5802/7677), in its own format. The password is ASCII, for
// which SASLprep changes nothing.
func scramSHA256Verifier(password string, salt []byte) (string, error) {
	salted, err := pbkdf2.Key(sha256.New, password, salt, scramIterations, sha256.Size)
	if err != nil {
		return "", fmt.Errorf("derive SCRAM key: %w", err)
	}
	clientKey := hmacSHA256(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(salted, "Server Key")
	encode := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", scramIterations, encode(salt), encode(storedKey[:]), encode(serverKey)), nil
}

func hmacSHA256(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return mac.Sum(nil)
}
