import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  countDocuments,
  createCollection,
  createIndex,
  deleteDocument,
  dropCollection,
  dropIndex,
  findDocuments,
  insertDocument,
  listCollections,
  listDatabases,
  listIndexes,
  replaceDocument,
  sampleDocuments,
  type CollectionRef,
  type IndexSpec,
} from '../api/documents';
import type { ServerQuery } from '../utils/mongoQuery';
import { api } from '../api/client';
import type { DatabaseInstance } from '../types';

const projectKey = (projectId: string) => ['documentdb', projectId] as const;
const databaseKey = (projectId: string, database: string) => [...projectKey(projectId), database] as const;
const collectionKey = (ref: CollectionRef) => [...databaseKey(ref.projectId, ref.database), ref.collection] as const;

// Whether a project was created with DocumentDB. It is fixed at creation, so
// this reads the project once rather than polling it.
export function useProjectIsDocumentDB(projectId: string) {
  return useQuery({
    queryKey: ['instance', projectId],
    queryFn: async () => (await api.get<DatabaseInstance>(`/provision/${projectId}`)).data,
    enabled: !!projectId,
    staleTime: Infinity,
    select: (instance) => instance.documentDb === true,
  });
}

const hasCollection = (ref: CollectionRef) => !!ref.projectId && !!ref.database && !!ref.collection;

export function useDocumentDatabases(projectId: string, enabled: boolean) {
  return useQuery({
    queryKey: [...projectKey(projectId), 'databases'],
    queryFn: () => listDatabases(projectId),
    enabled: enabled && !!projectId,
  });
}

export function useDocumentCollections(projectId: string, database: string) {
  return useQuery({
    queryKey: [...databaseKey(projectId, database), 'collections'],
    queryFn: () => listCollections(projectId, database),
    enabled: !!projectId && !!database,
  });
}

export function useDocumentPage(ref: CollectionRef, query: ServerQuery, page: { limit: number; skip: number }) {
  return useQuery({
    queryKey: [...collectionKey(ref), 'documents', query, page],
    queryFn: () => findDocuments(ref, query, page),
    enabled: hasCollection(ref),
  });
}

export function useDocumentCount(ref: CollectionRef, filter?: string) {
  return useQuery({
    queryKey: [...collectionKey(ref), 'count', filter ?? ''],
    queryFn: () => countDocuments(ref, filter),
    enabled: hasCollection(ref),
  });
}

export function useDocumentSample(ref: CollectionRef) {
  return useQuery({
    queryKey: [...collectionKey(ref), 'sample'],
    queryFn: () => sampleDocuments(ref),
    enabled: hasCollection(ref),
    staleTime: 30_000,
  });
}

export function useDocumentIndexes(ref: CollectionRef, enabled: boolean) {
  return useQuery({
    queryKey: [...collectionKey(ref), 'indexes'],
    queryFn: () => listIndexes(ref),
    enabled: enabled && hasCollection(ref),
  });
}

function useCollectionMutation<T>(ref: CollectionRef, run: (vars: T) => Promise<unknown>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: run,
    onSuccess: () => qc.invalidateQueries({ queryKey: collectionKey(ref) }),
  });
}

export function useInsertDocument(ref: CollectionRef) {
  return useCollectionMutation(ref, (text: string) => insertDocument(ref, text));
}

export function useReplaceDocument(ref: CollectionRef) {
  return useCollectionMutation(ref, ({ id, text }: { id: string; text: string }) => replaceDocument(ref, id, text));
}

export function useDeleteDocument(ref: CollectionRef) {
  return useCollectionMutation(ref, (id: string) => deleteDocument(ref, id));
}

export function useCreateIndex(ref: CollectionRef) {
  return useCollectionMutation(ref, (spec: IndexSpec) => createIndex(ref, spec));
}

export function useDropIndex(ref: CollectionRef) {
  return useCollectionMutation(ref, (name: string) => dropIndex(ref, name));
}

export function useCreateCollection(projectId: string, database: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (collection: string) => createCollection({ projectId, database, collection }),
    onSuccess: () => qc.invalidateQueries({ queryKey: projectKey(projectId) }),
  });
}

export function useDropCollection(projectId: string, database: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (collection: string) => dropCollection({ projectId, database, collection }),
    onSuccess: () => qc.invalidateQueries({ queryKey: projectKey(projectId) }),
  });
}
