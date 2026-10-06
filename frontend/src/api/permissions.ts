import { api } from './client';
import type { BoolExp } from '../utils/permissionRules';
import { serverErrorMessage } from '../utils/serverError';

// API permissions (EXC-370, docs/features/permissions.md): one object per
// (table, role, operation), tracked functions and function permissions. The
// control plane stores them; the engine reads the whole document.

export const OPERATIONS = ['select', 'insert', 'update', 'delete'] as const;
export type Operation = (typeof OPERATIONS)[number];

export type ColumnList = '*' | string[];
/** A preset value: a literal or a session variable name. */
export type Presets = Record<string, string | number | boolean>;

export interface SelectPermission {
  filter: BoolExp;
  columns: ColumnList;
  limit?: number | null;
  allowAggregations?: boolean;
}

export interface InsertPermission {
  check: BoolExp;
  columns: ColumnList;
  set?: Presets;
}

export interface UpdatePermission {
  filter: BoolExp;
  check?: BoolExp;
  columns: ColumnList;
  set?: Presets;
}

export interface DeletePermission {
  filter: BoolExp;
}

export interface PermissionByOperation {
  select: SelectPermission;
  insert: InsertPermission;
  update: UpdatePermission;
  delete: DeletePermission;
}

export type AnyPermission = PermissionByOperation[Operation];

export interface TablePermissions {
  table: string;
  role: string;
  select?: SelectPermission;
  insert?: InsertPermission;
  update?: UpdatePermission;
  delete?: DeletePermission;
}

export type ExposedAs = 'QUERY' | 'MUTATION';

export interface TrackedFunction {
  function: string;
  exposedAs: ExposedAs;
  inferPermissions: boolean;
  sessionArgument: string | null;
}

export interface TrackedFunctionAnswer extends TrackedFunction {
  securityDefiner: boolean;
}

export interface FunctionPermission {
  function: string;
  role: string;
}

export interface PermissionDocument {
  projectId: string;
  version: number;
  tables: TablePermissions[];
  functions: TrackedFunction[];
  functionPermissions: FunctionPermission[];
}

export interface TrackFunctionRequest {
  function: string;
  inferPermissions: boolean;
  sessionArgument: string | null;
}

const segment = encodeURIComponent;
const base = (projectId: string) => `/provision/${segment(projectId)}`;

export async function getPermissionDocument(projectId: string): Promise<PermissionDocument> {
  return (await api.get<PermissionDocument>(`${base(projectId)}/permissions/`)).data;
}

const tablePermissionPath = (projectId: string, table: string, role: string, operation: Operation) =>
  `${base(projectId)}/permissions/tables/${segment(table)}/roles/${segment(role)}/${operation}`;

export async function putTablePermission<O extends Operation>(
  projectId: string,
  table: string,
  role: string,
  operation: O,
  permission: PermissionByOperation[O],
): Promise<PermissionByOperation[O]> {
  return (await api.put<PermissionByOperation[O]>(tablePermissionPath(projectId, table, role, operation), permission))
    .data;
}

export async function deleteTablePermission(
  projectId: string,
  table: string,
  role: string,
  operation: Operation,
): Promise<void> {
  await api.delete(tablePermissionPath(projectId, table, role, operation));
}

export async function trackFunction(projectId: string, request: TrackFunctionRequest): Promise<TrackedFunctionAnswer> {
  return (await api.post<TrackedFunctionAnswer>(`${base(projectId)}/tracked-functions/`, request)).data;
}

export async function untrackFunction(projectId: string, fn: string): Promise<void> {
  await api.delete(`${base(projectId)}/tracked-functions/${segment(fn)}`);
}

const functionPermissionPath = (projectId: string, fn: string, role: string) =>
  `${base(projectId)}/function-permissions/${segment(fn)}/roles/${segment(role)}`;

export async function putFunctionPermission(projectId: string, fn: string, role: string): Promise<void> {
  await api.put(functionPermissionPath(projectId, fn, role));
}

export async function deleteFunctionPermission(projectId: string, fn: string, role: string): Promise<void> {
  await api.delete(functionPermissionPath(projectId, fn, role));
}

/** The server's own message for a refused call, else the transport's. */
export function apiErrorMessage(err: unknown, fallback: string): string {
  return serverErrorMessage(err, fallback);
}
