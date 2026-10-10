import { useQuery } from '@tanstack/react-query';
import { api } from './client';

// The MySQL catalogue, as GET /api/mysql/catalog serves it (ADR 0041). Like
// the PostgreSQL one, Studio keeps no copy: the control plane decides.
export interface MySQLCatalog {
  defaultVersion: string;
  versions: { version: string }[];
}

export const getMySQLCatalog = async (): Promise<MySQLCatalog> =>
  (await api.get<MySQLCatalog>('/mysql/catalog')).data;

// Only asked for while MySQL is on: with the feature off the route is a 404.
export const useMySQLCatalog = (enabled: boolean) =>
  useQuery({
    queryKey: ['mysql-catalog'],
    queryFn: getMySQLCatalog,
    staleTime: 60_000,
    enabled,
  });
