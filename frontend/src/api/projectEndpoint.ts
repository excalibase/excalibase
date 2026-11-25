import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// A project's database endpoint, as GET /api/projects/{projectId}/db-endpoint
// serves it (EXC-410).
//
// The control plane owns every part of this: the public host, the public
// port, whether the port is answering right now, the TLS posture and the
// cluster CA. Studio prints what it is told and decides none of it.
//
// The in-cluster address is never sent to an ordinary user (EXC-759): an app
// hosted beside the database gets DATABASE_URL injected, so there is nothing
// for Studio to show. Only a platform admin's response carries it, and no page
// reads it.

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
// behind the public port is serving.
export interface ProjectMongoEndpoint {
  available: boolean;
  port?: number;
}

export interface ProjectEndpoint {
  projectId: string;
  // False on an installation that offers no public ports: only the
  // in-cluster endpoint and the CA are described.
  publicOffered?: boolean;
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
  // The cluster CA, so full verification works, internal or public. Empty
  // while the database is not running.
  caCertificate: string;
  internal?: ProjectEndpointInternal;
  mongo?: ProjectMongoEndpoint;
  // A project on one Docker or Podman host: its ports are on the host's
  // 127.0.0.1, reached from elsewhere over an SSH tunnel (EXC-576).
  singleHost?: boolean;
  // Whether this caller may open or close the public port (Admin and up).
  // Only an explicit true shows the control; the server refuses writes anyway.
  canChange?: boolean;
}

// The wire shape carries the Mongo half as flat fields beside the Postgres ones.
interface ProjectEndpointWire extends Omit<ProjectEndpoint, 'mongo'> {
  mongoPort?: number;
  mongoAvailable?: boolean;
}

function mongoOf(wire: ProjectEndpointWire): ProjectMongoEndpoint | undefined {
  if (!wire.mongoPort) return undefined;
  return { available: wire.mongoAvailable === true, port: wire.mongoPort };
}

function fromWire(wire: ProjectEndpointWire): ProjectEndpoint {
  const mongo = mongoOf(wire);
  return mongo ? { ...wire, mongo } : wire;
}

export const getProjectEndpoint = async (projectId: string): Promise<ProjectEndpoint> =>
  fromWire((await api.get<ProjectEndpointWire>(`/projects/${projectId}/db-endpoint`)).data);

// useProjectEndpoint reads one project's endpoint. A failure is not retried
// and not surfaced as an error: the pages that use it fall back to the
// details they already hold, which is the honest answer when the public
// endpoint cannot be read.
export const useProjectEndpoint = (projectId: string | undefined) =>
  useQuery({
    queryKey: ['project-endpoint', projectId],
    queryFn: () => getProjectEndpoint(projectId as string),
    enabled: !!projectId,
    retry: false,
    staleTime: 30_000,
  });

// Opening the port is an Admin decision; the server refuses anyone else.
export const setProjectEndpointPublic = async (
  projectId: string,
  publicEnabled: boolean,
): Promise<ProjectEndpoint> =>
  fromWire(
    (await api.put<ProjectEndpointWire>(`/projects/${projectId}/db-endpoint`, { publicEnabled })).data,
  );

export const useSetProjectEndpointPublic = (projectId: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (publicEnabled: boolean) => setProjectEndpointPublic(projectId, publicEnabled),
    onSuccess: (endpoint) => queryClient.setQueryData(['project-endpoint', projectId], endpoint),
  });
};
