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

// The Mongo half of a DocumentDB project. `available` is whether the gateway
// behind the public port is serving; the server reports no readiness for the
// internal address, which exists for every DocumentDB project.
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

// The wire shape carries the Mongo half as flat fields beside the Postgres ones.
interface ProjectEndpointWire extends Omit<ProjectEndpoint, 'mongo'> {
  mongoPort?: number;
  mongoAvailable?: boolean;
  internal: ProjectEndpointInternal & { mongoPort?: number };
}

function mongoOf(wire: ProjectEndpointWire): ProjectMongoEndpoint | undefined {
  const internalPort = wire.internal.mongoPort;
  if (!wire.mongoPort && !internalPort) return undefined;
  return {
    available: wire.mongoAvailable === true,
    ...(wire.mongoPort ? { port: wire.mongoPort } : {}),
    ...(internalPort ? { internal: { host: wire.internal.host, port: internalPort } } : {}),
  };
}

export const getProjectEndpoint = async (projectId: string): Promise<ProjectEndpoint> => {
  const wire = (await api.get<ProjectEndpointWire>(`/projects/${projectId}/db-endpoint`)).data;
  const mongo = mongoOf(wire);
  return mongo ? { ...wire, mongo } : wire;
};

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
