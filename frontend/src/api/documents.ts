import { api } from './client';
import type { ServerQuery } from '../utils/mongoQuery';

export type MongoDocument = Record<string, unknown>;

export interface DocumentCollection {
  name: string;
  type: string;
}

export interface DocumentPage {
  documents: MongoDocument[];
  limit: number;
  skip: number;
}

export interface IndexSpec {
  keys: Record<string, 1 | -1 | string>;
  name?: string;
  unique?: boolean;
}

export interface CollectionRef {
  projectId: string;
  database: string;
  collection: string;
}

const base = (projectId: string) => `/projects/${projectId}/documentdb/databases`;

const collectionPath = ({ projectId, database, collection }: CollectionRef) =>
  `${base(projectId)}/${encodeURIComponent(database)}/collections/${encodeURIComponent(collection)}`;

const jsonBody = { headers: { 'Content-Type': 'application/json' } };

export async function listDatabases(projectId: string): Promise<string[]> {
  const { data } = await api.get<{ databases: string[] }>(base(projectId));
  return data.databases;
}

export async function listCollections(projectId: string, database: string): Promise<DocumentCollection[]> {
  const { data } = await api.get<{ collections: DocumentCollection[] }>(
    `${base(projectId)}/${encodeURIComponent(database)}/collections`,
  );
  return data.collections;
}

export async function createCollection(ref: CollectionRef): Promise<void> {
  await api.post(`${base(ref.projectId)}/${encodeURIComponent(ref.database)}/collections`, { name: ref.collection });
}

export async function dropCollection(ref: CollectionRef): Promise<void> {
  await api.delete(`${collectionPath(ref)}/`);
}

export async function findDocuments(
  ref: CollectionRef,
  query: ServerQuery,
  page: { limit: number; skip: number },
): Promise<DocumentPage> {
  const { data } = await api.get<DocumentPage>(`${collectionPath(ref)}/documents`, {
    params: { ...query, limit: page.limit, skip: page.skip },
  });
  return data;
}

export async function countDocuments(ref: CollectionRef, filter?: string): Promise<number> {
  const { data } = await api.get<{ count: number }>(`${collectionPath(ref)}/count`, {
    params: filter ? { filter } : {},
  });
  return data.count;
}

export async function insertDocument(ref: CollectionRef, documentText: string): Promise<unknown> {
  const { data } = await api.post<{ insertedId: unknown }>(`${collectionPath(ref)}/documents`, documentText, jsonBody);
  return data.insertedId;
}

export async function replaceDocument(ref: CollectionRef, id: string, documentText: string): Promise<void> {
  await api.put(`${collectionPath(ref)}/documents`, documentText, { ...jsonBody, params: { id } });
}

export async function deleteDocument(ref: CollectionRef, id: string): Promise<void> {
  await api.delete(`${collectionPath(ref)}/documents`, { params: { id } });
}

export async function listIndexes(ref: CollectionRef): Promise<MongoDocument[]> {
  const { data } = await api.get<{ indexes: MongoDocument[] }>(`${collectionPath(ref)}/indexes`);
  return data.indexes;
}

export async function createIndex(ref: CollectionRef, spec: IndexSpec): Promise<string> {
  const { data } = await api.post<{ name: string }>(`${collectionPath(ref)}/indexes`, spec);
  return data.name;
}

export async function dropIndex(ref: CollectionRef, name: string): Promise<void> {
  await api.delete(`${collectionPath(ref)}/indexes/${encodeURIComponent(name)}`);
}

export async function sampleDocuments(ref: CollectionRef): Promise<MongoDocument[]> {
  const { data } = await api.get<{ documents: MongoDocument[] }>(`${collectionPath(ref)}/sample`);
  return data.documents;
}

// The server's own words for a refused request, which name the problem with
// the query or document rather than the transport.
export function apiErrorMessage(err: unknown): string {
  const response = (err as { response?: { data?: { error?: string } } })?.response;
  return response?.data?.error ?? (err as Error)?.message ?? 'Request failed';
}
