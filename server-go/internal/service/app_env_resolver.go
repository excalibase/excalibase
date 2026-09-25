package service

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

// AppEnvResolver reads, at deploy time, the values an app's variables point
// at. A database reference resolves to the project's own login — the one its
// connection strings show — never the platform's excalibase_app role.
type AppEnvResolver struct {
	vault     vaultclient.VaultClient
	instances storage.InstanceStore
}

var _ k8s.Resolver = (*AppEnvResolver)(nil)

func NewAppEnvResolver(vault vaultclient.VaultClient, instances storage.InstanceStore) *AppEnvResolver {
	return &AppEnvResolver{vault: vault, instances: instances}
}

var errNoVault = errors.New("no vault is configured to read it from")

type databaseLogin struct {
	host, port, database, username, password, sslMode string
}

func (r *AppEnvResolver) ResolveReference(projectID string, ref apphost.ResolvedReference) (string, error) {
	if ref.Scope != apphost.ScopeInternal {
		return "", errors.New("only the in-cluster database address can be given to a container")
	}
	if ref.Target.SourceKind != apphost.SourceDatabase {
		return "", fmt.Errorf("unknown source kind %q", ref.Target.SourceKind)
	}
	login, err := r.databaseLogin(projectID, ref.Target.SourceName)
	if err != nil {
		return "", err
	}
	switch ref.Target.Variable {
	case "DATABASE_URL":
		return login.url(), nil
	case "PGHOST":
		return login.host, nil
	case "PGPORT":
		return login.port, nil
	case "PGDATABASE":
		return login.database, nil
	case "PGUSER":
		return login.username, nil
	case "PGPASSWORD":
		return login.password, nil
	default:
		return "", fmt.Errorf("a database exposes no variable %q", ref.Target.Variable)
	}
}

func (r *AppEnvResolver) databaseLogin(projectID, databaseName string) (databaseLogin, error) {
	inst, err := r.instances.FindByProjectID(projectID)
	if err != nil {
		log.Printf("app env: look up project %s: %v", projectID, err)
		return databaseLogin{}, errors.New("could not look up the project's database")
	}
	if inst == nil || inst.DatabaseName == "" {
		return databaseLogin{}, errors.New("the project has no database")
	}
	if inst.DatabaseName != databaseName {
		return databaseLogin{}, fmt.Errorf("the project has no database named %q", databaseName)
	}
	if domain.IsNotServable(inst.Status) || domain.IsBuildingStatus(inst.Status) {
		return databaseLogin{}, fmt.Errorf("the project's database is not available (%s)", inst.Status)
	}
	if inst.SSLMode == "" {
		return databaseLogin{}, errors.New("the project's database has no TLS setting recorded")
	}
	if r.vault == nil {
		return databaseLogin{}, errNoVault
	}
	record, err := r.vault.Get(vaultCredentialPath(projectID, roleAdmin))
	if err != nil {
		log.Printf("app env: read owner login for %s: %v", projectID, err)
		return databaseLogin{}, errors.New("could not read the project's database login")
	}
	login := databaseLogin{
		host: record["host"], port: record["port"], database: record["database"],
		username: record["username"], password: record["password"], sslMode: inst.SSLMode,
	}
	for field, value := range map[string]string{
		"host": login.host, "port": login.port, "database": login.database,
		"username": login.username, "password": login.password,
	} {
		if value == "" {
			return databaseLogin{}, fmt.Errorf("the stored database login has no %s", field)
		}
	}
	return login, nil
}

func (l databaseLogin) url() string {
	u := url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(l.username, l.password),
		Host:     net.JoinHostPort(l.host, l.port),
		Path:     "/" + l.database,
		RawQuery: url.Values{"sslmode": {l.sslMode}}.Encode(),
	}
	return u.String()
}

// ResolveSecret reads the value at the app's own vault entry. The prefix is
// checked again here, since this read happens with the platform's authority.
func (r *AppEnvResolver) ResolveSecret(projectID string, ref apphost.SecretRef) (string, error) {
	if !strings.HasPrefix(ref.Path, "projects/"+projectID+"/apps/") {
		return "", errors.New("the secret is not one of this project's app secrets")
	}
	if r.vault == nil {
		return "", errNoVault
	}
	record, err := r.vault.Get(ref.Path)
	if err != nil {
		log.Printf("app env: read secret %s: %v", ref.Path, err)
		return "", errors.New("no value is stored")
	}
	if record[ref.Key] == "" {
		return "", errors.New("no value is stored")
	}
	return record[ref.Key], nil
}
