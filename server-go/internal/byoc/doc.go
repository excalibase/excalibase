// Package byoc guards every outbound connection the platform makes to a
// bring-your-own-cloud (externally managed) Postgres.
//
// # Threat model
//
// BYOC credentials are supplied by an ordinary project member, stored in
// vault, and later used by the platform's own outbound connections (schema
// browser, realtime publication management, migrations). Without a guard a
// user could point a "database" at 127.0.0.1, an RFC-1918 address, the cloud
// metadata endpoint or a cluster-internal service and turn the platform into
// a stored-SSRF pivot. The guard closes the following vectors:
//
//   - Internal ranges, IPv4 and IPv6: loopback, unspecified, RFC-1918, CGNAT
//     (100.64/10), link-local (169.254/16, fe80::/10), IPv6 ULA (fc00::/7),
//     deprecated site-local (fec0::/10), multicast, reserved/benchmark space,
//     the discard prefix and cloud metadata addresses (169.254.169.254,
//     fd00:ec2::254). See classify.go.
//   - Encapsulated IPv4: v4-mapped (::ffff:a.b.c.d), v4-compatible (::a.b.c.d),
//     NAT64 (64:ff9b::/96) and 6to4 (2002::/16) are unwrapped and the embedded
//     IPv4 is classified; Teredo (2001::/32) is refused outright.
//   - DNS rebinding / TOCTOU: registration validates the name, but the
//     enforcement point is the dialer. Guard.DialTimeout resolves the host,
//     classifies every answer, and dials exactly the address it classified,
//     so a name that re-resolves to an internal IP after registration is
//     refused at connect time. TLS is negotiated by lib/pq on top of the
//     returned conn with ServerName taken from the DSN host, so certificate
//     verification (sslmode=verify-full) still checks the hostname.
//   - Connection-string re-routing: the host must be a single hostname or IP
//     literal (no comma-separated multi-host lists, no port, no zone id, no
//     unix socket path), and username/database/password may not contain the
//     characters that would let them smuggle `host=`/`hostaddr=` into a
//     keyword DSN or re-route a URL DSN. See credentials.go. Only tcp is
//     dialled; unix sockets are refused.
//   - Operator egress allowlist: BYOC_EGRESS_ALLOWLIST (comma-separated
//     CIDRs, IPs, hostnames or *.suffix wildcards) restricts BYOC targets to
//     the operator's expected destinations. It is an additional constraint:
//     an allowlisted private range is still refused. See policy.go.
//
// No HTTP request is made in the BYOC flow, so there is no redirect surface;
// any future HTTP probe of a BYOC target must use a client that refuses
// redirects and dials through this guard.
//
// User-facing errors never include resolved addresses.
package byoc
