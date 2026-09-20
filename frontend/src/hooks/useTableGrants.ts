import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../api/client';
import { onMutationError } from '../utils/mutationHelpers';

/**
 * Table exposure — which of a project's tables its END USERS can reach through
 * the generated GraphQL/REST API (EXC-400).
 *
 * Enforcement is on for every project, so a table with no grant is absent from
 * the schema the engine serves an end user. That is why this is one toggle and
 * not a permission matrix: the question it answers is "is this table part of
 * the project's public API at all". WHICH ROWS a caller may see stays with RLS,
 * where a claim-driven rule belongs.
 *
 * None of this touches the developer. Studio browses tables and runs SQL
 * through the control plane's own /schema routes, which read the tenant
 * database directly and never consult a grant.
 */

/** The only roles a grant may name: an end user is signed in, or is not. */
export const END_USER_ROLES = ['anon', 'authenticated'] as const;
export type EndUserRole = (typeof END_USER_ROLES)[number];

/**
 * Every operation, granted together. A table is in the API or it is not; a
 * read-only or write-only surface is an RLS policy, not a narrower grant.
 */
const ALL_OPERATIONS = ['SELECT', 'INSERT', 'UPDATE', 'DELETE'] as const;

export interface TableGrant {
  id: string;
  projectId: string;
  resource: string;
  operations: string[];
  role: string;
  enabled: boolean;
}

export interface TableGrantSet {
  projectId: string;
  /**
   * Always sent explicitly by the control plane, never inferred from an empty
   * grants array: `enforced: true` with no grants means "deny everything".
   * False only while an operator has switched exposure off installation-wide.
   */
  enforced: boolean;
  grants: TableGrant[];
}

const grantsKey = (projectId: string) => ['table-grants', projectId] as const;
const grantsPath = (projectId: string) => `/provision/${projectId}/table-grants/`;

export function useTableGrants(projectId: string) {
  return useQuery<TableGrantSet>({
    queryKey: grantsKey(projectId),
    queryFn: async () => {
      const { data } = await api.get<TableGrantSet>(grantsPath(projectId));
      return data;
    },
    enabled: !!projectId,
    staleTime: 30_000,
  });
}

/**
 * Grants are stored either schema-qualified or bare, and the engine resolves a
 * bare name against the real schema. Both spellings count as a match here so
 * the toggle reflects what the engine will actually do.
 */
function grantsFor(set: TableGrantSet | undefined, schema: string, table: string): TableGrant[] {
  if (!set) return [];
  return set.grants.filter(
    (g) => g.resource === `${schema}.${table}` || (schema === 'public' && g.resource === table),
  );
}

/** True when at least one enabled grant puts this table in the API surface. */
export function isTableExposed(
  set: TableGrantSet | undefined,
  schema: string,
  table: string,
): boolean {
  return grantsFor(set, schema, table).some((g) => g.enabled);
}

export interface SetTableExposedVars {
  schema: string;
  table: string;
  exposed: boolean;
}

/**
 * Turns one table's reachability on or off. Exposing adds the grants that are
 * missing rather than replacing the set, so a grant an operator narrowed by
 * hand is left as it is; hiding removes every grant on that table, because a
 * half-removed exposure is the ambiguity this feature exists to end.
 */
export function useSetTableExposed(projectId: string) {
  const qc = useQueryClient();
  return useMutation<void, Error, SetTableExposedVars>({
    mutationFn: async ({ schema, table, exposed }) => {
      const { data: set } = await api.get<TableGrantSet>(grantsPath(projectId));
      const existing = grantsFor(set, schema, table);

      if (!exposed) {
        await Promise.all(
          existing.map((g) => api.delete(`/provision/${projectId}/table-grants/${g.id}`)),
        );
        return;
      }

      const missing = END_USER_ROLES.filter((role) => !existing.some((g) => g.role === role));
      await Promise.all(
        missing.map((role) =>
          api.post(grantsPath(projectId), {
            resource: `${schema}.${table}`,
            operations: [...ALL_OPERATIONS],
            role,
            enabled: true,
          }),
        ),
      );
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: grantsKey(projectId) });
    },
    onError: onMutationError('update API exposure'),
  });
}
