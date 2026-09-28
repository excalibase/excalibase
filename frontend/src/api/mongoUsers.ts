import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// A DocumentDB project's own Mongo users (EXC-427). They log in over the Mongo
// protocol only; the password is in the create and rotate answers and nowhere
// else.
export type MongoUserRole = 'readWrite' | 'read';

export interface MongoUser {
  username: string;
  role: MongoUserRole;
  createdAt: string;
}

export interface MongoUserCredential {
  username: string;
  role: MongoUserRole;
  password: string;
}

export const ROLE_LABELS: Record<MongoUserRole, string> = {
  readWrite: 'Read-write',
  read: 'Read-only',
};

// Mirrors the server's rule so an obvious typo is caught before sending; the
// server still decides, including the reserved names.
export const MONGO_USERNAME_PATTERN = /^[a-z][a-z0-9_]{2,62}$/;

export function mongoUserUri(
  username: string,
  password: string,
  host: string,
  port: number,
  tls: boolean,
): string {
  return `mongodb://${username}:${encodeURIComponent(password)}@${host}:${port}/?tls=${tls}&authMechanism=SCRAM-SHA-256`;
}

const usersPath = (projectId: string) => `/provision/${projectId}/documentdb/users`;
const usersKey = (projectId: string) => ['mongo-users', projectId] as const;

export function useMongoUsers(projectId: string, enabled: boolean) {
  return useQuery({
    queryKey: usersKey(projectId),
    queryFn: async () =>
      (await api.get<{ users: MongoUser[]; limit: number }>(usersPath(projectId))).data,
    enabled: enabled && !!projectId,
  });
}

export function useCreateMongoUser(projectId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (body: { username: string; role: MongoUserRole }) =>
      (await api.post<MongoUserCredential>(usersPath(projectId), body)).data,
    onSuccess: () => client.invalidateQueries({ queryKey: usersKey(projectId) }),
  });
}

export function useRotateMongoUser(projectId: string) {
  return useMutation({
    mutationFn: async (username: string) =>
      (
        await api.post<MongoUserCredential>(
          `${usersPath(projectId)}/${encodeURIComponent(username)}/rotate`,
        )
      ).data,
  });
}

export function useDeleteMongoUser(projectId: string) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (username: string) => {
      await api.delete(`${usersPath(projectId)}/${encodeURIComponent(username)}`);
    },
    onSuccess: () => client.invalidateQueries({ queryKey: usersKey(projectId) }),
  });
}
