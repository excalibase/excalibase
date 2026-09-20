import { useQuery } from '@tanstack/react-query';
import { api } from './client';

// A project's database endpoint, as GET /api/projects/{projectId}/db-endpoint
// serves it (EXC-410).
//
// The control plane owns every part of this: the public host, the public
// port, whether the port is answering right now, the TLS posture and the
// cluster CA. Studio prints what it is told and decides none of it.
//
// The in-cluster endpoint is always present, whether or not the project
// publishes a public port: an application hosted beside the database should
// connect there and needs no public port at all.

export interface ProjectEndpointConnectionStrings {
  // Passwordless libpq URIs the control plane renders for each TLS choice.
  // Studio shows what the customer can actually paste — password included —
  // so it builds its own strings; these are kept so a caller that wants the
  // canonical, secret-free form has it.
  requireTls: string;
  allowPlaintext: string;
}

export interface ProjectEndpointInternal {
  host: string;
  port: number;
  connectionString: string;
}

// ---------------------------------------------------------------------------
// SEAM: the Mongo half of a DocumentDB project (EXC-409, PR 59, unmerged)
//
// A DocumentDB project is one database with one credential answering two
// protocols. The endpoint API on main reports the Postgres half only; the
// gateway that answers Mongo clients is still on an open pull request, and
// nothing served by main names a Mongo port or says whether the gateway is
// up.
//
// So `mongo` is absent today, and a DocumentDB project honestly says its
// Mongo endpoint is not answering yet rather than printing a port nobody
// allocated. What EXC-409 has to add to the db-endpoint response to close
// this seam, and nothing more:
//
//   mongo.available  whether the gateway is up AND has created its user.
//                    It is false for a while after Postgres already answers:
//                    the gateway waits for the database first.
//   mongo.port       the public port Mongo clients dial. Absent when the
//                    project publishes no public port.
//   mongo.internal   the in-cluster host and port of the gateway.
//
// Closing the seam is: serve the field, nothing here changes shape.
// ---------------------------------------------------------------------------
export interface ProjectMongoEndpoint {
  available: boolean;
  port?: number;
  internal?: { host: string; port: number };
}

export interface ProjectEndpoint {
  projectId: string;
  // The customer's choice: false means no public Service exists at all.
  publicEnabled: boolean;
  // Observation, not choice: whether the public port is answering right now.
  // False while the project is paused, deleting or restoring.
  available: boolean;
  host: string;
  port: number;
  requireTls: boolean;
  database: string;
  username: string;
  connectionStrings: ProjectEndpointConnectionStrings;
  // The cluster CA, so full verification works. Empty unless the endpoint is
  // actually up.
  caCertificate: string;
  internal: ProjectEndpointInternal;
  mongo?: ProjectMongoEndpoint;
}

export const getProjectEndpoint = async (projectId: string): Promise<ProjectEndpoint> =>
  (await api.get<ProjectEndpoint>(`/projects/${projectId}/db-endpoint`)).data;

// useProjectEndpoint reads one project's endpoint. A failure is not retried
// and not surfaced as an error: the pages that use it fall back to the
// in-cluster details they already hold, which is the honest answer when the
// public endpoint cannot be read.
export const useProjectEndpoint = (projectId: string | undefined) =>
  useQuery({
    queryKey: ['project-endpoint', projectId],
    queryFn: () => getProjectEndpoint(projectId as string),
    enabled: !!projectId,
    retry: false,
    staleTime: 30_000,
  });
