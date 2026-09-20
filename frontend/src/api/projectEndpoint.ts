// ---------------------------------------------------------------------------
// SEAM: the external database endpoint (EXC-410)
//
// EXC-410 owns the public host, the public port and the TLS posture of a
// project's database, and is being written concurrently. Studio does not
// invent any of it. This file is the whole of the contract Studio consumes:
// a component takes a ProjectEndpoint or takes none.
//
// Until EXC-410 lands, nothing supplies one, and every consumer falls back to
// the in-cluster details GET /api/provision/{projectId}/credentials already
// returns — labelled as in-cluster, so nobody copies a public address that
// does not exist.
//
// What EXC-410 needs to provide for this seam to close:
//   * a read endpoint, per project, returning the fields below;
//   * `host` and `port`: the public Postgres target, whatever routing model
//     it settles on (a port per tenant, at the time of writing);
//   * `mongoPort`: the public port Mongo clients reach a DocumentDB project
//     on. Absent means no Mongo endpoint is published; Studio then says so
//     rather than guessing a port;
//   * `sslMode`: the libpq sslmode the customer should actually use against
//     that endpoint — Studio prints it, it does not decide it;
//   * `caCertPem`: the PEM the customer needs for `verify-full`. Absent means
//     no CA download is offered.
//
// Closing the seam is: fetch it, pass it in. No consumer changes shape.
// ---------------------------------------------------------------------------

export interface ProjectEndpoint {
  // Public DNS name of the project's database.
  host: string;
  // Public Postgres port.
  port: number;
  // Public port for Mongo clients on a DocumentDB project; absent when none
  // is published.
  mongoPort?: number;
  // libpq sslmode for this endpoint (e.g. "require", "verify-full").
  sslMode?: string;
  // PEM-encoded certificate authority for full verification; absent when the
  // platform publishes none.
  caCertPem?: string;
}
