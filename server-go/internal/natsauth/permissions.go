// Package natsauth models who may publish and subscribe on the platform's
// NATS bus, and answers the server's auth_callout requests with a signed,
// per-principal user JWT carrying exactly those subject permissions.
//
// Before EXC-324 every client shared one (or no) credential, so any
// per-tenant watcher pod — which runs inside the tenant namespace and is
// therefore reachable by tenant workloads — could subscribe to `cdc.>` and
// read every other tenant's change stream. The permission matrix below is
// the fix: a tenant watcher may publish only under its own project prefix
// and may subscribe to nothing except its own reply inbox.
package natsauth

import (
	"fmt"
	"regexp"
	"strings"
)

// Service principals. These are fixed names minted at platform bootstrap.
const (
	PrincipalGraphQL      = "svc-graphql"
	PrincipalProvisioning = "svc-provisioning"
	PrincipalPgDog        = "svc-pgdog"
)

// tenantWatcherPrefix names the per-project CDC watcher principal family;
// the full principal is tenantWatcherPrefix + projectID.
const tenantWatcherPrefix = "tenant-watcher:"

// Subjects the platform uses. Kept as constants so the matrix, the tests and
// the callout all agree on one spelling.
const (
	// SubjectPgDogReload is the single signal PgDog listens for.
	SubjectPgDogReload = "pgdog.config.reload"

	subjectCDCAll      = "cdc.>"
	subjectPoliciesAll = "policies.>"
	subjectPgDogAll    = "pgdog.>"

	subjectJSInfo        = "$JS.API.INFO"
	subjectJSStreamAll   = "$JS.API.STREAM.>"
	subjectJSConsumerAll = "$JS.API.CONSUMER.>"
	subjectJSAckAll      = "$JS.ACK.>"
)

// inboxPrefixRoot keeps every principal's request/reply inbox in its own
// subject space. Without it each client would need `_INBOX.>`, which is a
// cross-tenant read channel: replies to other connections' requests land
// there too.
const inboxPrefixRoot = "_INBOX_"

// Permissions is the subject allow-list handed to the NATS server for one
// connection. Anything not listed is denied — there is no deny-list and no
// implicit grant.
type Permissions struct {
	Publish     []string
	Subscribe   []string
	InboxPrefix string
}

// safeToken accepts only what is safe to splice into a NATS subject: no
// dots (which would widen the token), no `*`/`>` wildcards, no whitespace.
var safeToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// TenantWatcherPrincipal is the principal name for a project's CDC watcher.
func TenantWatcherPrincipal(projectID string) string {
	return tenantWatcherPrefix + projectID
}

// ProjectIDForPrincipal reports the project a tenant-watcher principal is
// scoped to. Service principals return ok=false.
func ProjectIDForPrincipal(principal string) (string, bool) {
	projectID, found := strings.CutPrefix(principal, tenantWatcherPrefix)
	if !found || !safeToken.MatchString(projectID) {
		return "", false
	}
	return projectID, true
}

// PermissionsFor resolves a principal to its subject allow-list. It fails
// closed: an unknown service name, a malformed project id or an unsafe
// stream name yields an error and no permissions, and the callout then
// refuses the connection.
func PermissionsFor(principal, cdcStream string) (Permissions, error) {
	if !safeToken.MatchString(cdcStream) {
		return Permissions{}, fmt.Errorf("unsafe CDC stream name")
	}

	switch principal {
	case PrincipalGraphQL:
		// Read plane: consumes tenant CDC and policy invalidations across
		// every project it serves, and drives its own push consumers.
		return withInbox(principal, Permissions{
			Publish:   []string{subjectJSInfo, streamInfoSubject(cdcStream), subjectJSConsumerAll, subjectJSAckAll},
			Subscribe: []string{subjectCDCAll, subjectPoliciesAll},
		}), nil

	case PrincipalProvisioning:
		// Control plane: announces policy changes and PgDog reloads, and
		// provisions the shared CDC stream. Reads no tenant traffic.
		return withInbox(principal, Permissions{
			Publish: []string{subjectPoliciesAll, subjectPgDogAll, subjectJSInfo, subjectJSStreamAll},
		}), nil

	case PrincipalPgDog:
		// Pure listener on one subject; never publishes, never requests,
		// so it gets no inbox either.
		return Permissions{Subscribe: []string{SubjectPgDogReload}}, nil
	}

	projectID, ok := ProjectIDForPrincipal(principal)
	if !ok {
		return Permissions{}, fmt.Errorf("unknown NATS principal")
	}
	// Tenant watcher: publish-only, and only beneath its own project
	// prefix. Subscribing is limited to its own JetStream ack inbox.
	return withInbox(tenantInboxToken(projectID), Permissions{
		Publish: []string{
			fmt.Sprintf("cdc.%s.>", projectID),
			subjectJSInfo,
			streamInfoSubject(cdcStream),
		},
	}), nil
}

// InboxPrefixFor reports the request/reply inbox prefix a principal must
// configure on its client. Blank for an unknown principal and for one the
// matrix grants no inbox — in both cases the client sets no prefix.
func InboxPrefixFor(principal string) string {
	perms, err := PermissionsFor(principal, "CDC")
	if err != nil {
		return ""
	}
	return perms.InboxPrefix
}

func streamInfoSubject(cdcStream string) string {
	return "$JS.API.STREAM.INFO." + cdcStream
}

func tenantInboxToken(projectID string) string {
	return "tw_" + projectID
}

func withInbox(token string, perms Permissions) Permissions {
	perms.InboxPrefix = inboxPrefixRoot + token
	perms.Subscribe = append(perms.Subscribe, perms.InboxPrefix+".>")
	return perms
}
