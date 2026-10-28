package domain

// A project's read-only address (EXC-666): a second way into the database
// that reaches only its replicas, like an RDS reader endpoint. A replica is a
// Postgres hot standby or a MOCO super_read_only mysqld, so whoever logs in
// cannot write through it; a database with no replica has no read address.

// DBEndpointRoleReader is the public port the read address holds, from the
// same allocator and quarantine as the project's other ports.
const DBEndpointRoleReader DBEndpointRole = "reader"

// Why a project has no read address, as the API reports it.
const (
	// ReadAddressNoReplica is a database running one copy (Free).
	ReadAddressNoReplica = "one-copy"
	// ReadAddressSingleHost is a single Docker or Podman host, which runs one
	// copy of every database (ADR 0038).
	ReadAddressSingleHost = "single-host"
)

// DBReadAddressServiceName is the project's public reader LoadBalancer
// Service, beside (and distinct from) the primary one.
func DBReadAddressServiceName(projectID string, engine DatabaseType) string {
	if engine == MySQL {
		return projectID + "-mysql-read-public"
	}
	return projectID + "-postgres-read-public"
}

// ReplicasOf is how many copies serve reads beside the primary.
func ReplicasOf(copies int64) int {
	if copies <= 1 {
		return 0
	}
	return int(copies - 1)
}
