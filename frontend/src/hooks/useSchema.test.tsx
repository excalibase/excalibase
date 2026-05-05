import { describe, test, expect, beforeEach, vi } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import {
  useTables, useColumns, useRelationships, useCreateTable, useUpdateTable,
  useDropTable, useAddColumn, useAlterColumn, useDropColumn,
} from './useSchemaTables';
import {
  useRoles, useCreateRole, useDropRole, useExtensions, useCreateExtension,
  useDropExtension, usePolicies, useCreatePolicy, useDropPolicy,
  useFunctions, useCreateFunction, useDropFunction,
  useTriggers, useCreateTrigger, useDropTrigger, useIndexes,
} from './useSchemaObjects';
import { useExecuteQuery } from './useSchemaQuery';
import { useRows, useInsertRow, useUpdateRow, useDeleteRow } from './useSchemaData';
import { usePerformanceAdvisor, useSecurityAdvisor } from './useSchemaAdvisors';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

function makeWrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const Wrapper: React.FC<{ children: React.ReactNode }> = ({ children }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { client, Wrapper };
}

beforeEach(() => vi.clearAllMocks());

describe('useSchemaTables', () => {
  test('useTables GETs', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [] } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useTables('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/schema/p1/tables', { params: { schema: 'public' } });
  });

  test('useColumns GETs', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [] } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useColumns('p1', 'posts'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/schema/p1/tables/posts/columns', { params: { schema: 'public' } });
  });

  test('useRelationships GETs', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [] } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useRelationships('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/schema/p1/relationships', { params: { schema: 'public' } });
  });

  test('useCreateTable POSTs', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useCreateTable('p1'), { wrapper: Wrapper });
    result.current.mutate({ name: 't', columns: [] });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalled();
  });

  test('useUpdateTable PATCHes', async () => {
    vi.mocked(api.patch).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useUpdateTable('p1'), { wrapper: Wrapper });
    result.current.mutate({ tableName: 'posts', newName: 'p2' });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.patch).toHaveBeenCalled();
  });
});

describe('useSchemaObjects', () => {
  test('useRoles', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [] } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useRoles('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/schema/p1/roles');
  });

  test('useCreateRole', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useCreateRole('p1'), { wrapper: Wrapper });
    result.current.mutate({ name: 'r1' });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/schema/p1/roles', { name: 'r1' });
  });

  test('useDropRole', async () => {
    vi.mocked(api.delete).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useDropRole('p1'), { wrapper: Wrapper });
    result.current.mutate('r1');
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.delete).toHaveBeenCalledWith('/schema/p1/roles/r1');
  });

  test('useExtensions', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [] } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useExtensions('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/schema/p1/extensions');
  });

  test('useCreateExtension', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useCreateExtension('p1'), { wrapper: Wrapper });
    result.current.mutate({ name: 'pgvector' });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/schema/p1/extensions', { name: 'pgvector' });
  });
});

describe('useSchemaQuery', () => {
  test('useExecuteQuery POSTs query', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: { rows: [] } } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useExecuteQuery('p1'), { wrapper: Wrapper });
    result.current.mutate('SELECT 1');
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/schema/p1/query', { query: 'SELECT 1' });
  });
});

describe('useSchemaData', () => {
  test('useRows GETs', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: { rows: [] } } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useRows('p1', 'posts'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/schema/p1/tables/posts/rows', expect.any(Object));
  });

  test('useInsertRow', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: {} } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useInsertRow('p1'), { wrapper: Wrapper });
    result.current.mutate({ tableName: 'posts', data: { title: 'x' } });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/schema/p1/tables/posts/rows', { data: { title: 'x' } });
  });

  test('useUpdateRow', async () => {
    vi.mocked(api.patch).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useUpdateRow('p1'), { wrapper: Wrapper });
    result.current.mutate({ tableName: 'posts', pk: { column: 'id', value: '1' }, data: { title: 'y' } });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.patch).toHaveBeenCalled();
  });

  test('useDeleteRow', async () => {
    vi.mocked(api.delete).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useDeleteRow('p1'), { wrapper: Wrapper });
    result.current.mutate({ tableName: 'posts', pk: { column: 'id', value: '1' } });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.delete).toHaveBeenCalled();
  });
});

describe('useSchemaTables — extra DDL', () => {
  test('useDropTable DELETEs', async () => {
    vi.mocked(api.delete).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useDropTable('p1'), { wrapper: Wrapper });
    result.current.mutate({ tableName: 'posts' });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.delete).toHaveBeenCalled();
  });

  test('useAddColumn POSTs', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useAddColumn('p1'), { wrapper: Wrapper });
    result.current.mutate({ tableName: 'posts', name: 'c', type: 'text' });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.post).toHaveBeenCalled();
  });

  test('useAlterColumn PATCHes', async () => {
    vi.mocked(api.patch).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useAlterColumn('p1'), { wrapper: Wrapper });
    result.current.mutate({ tableName: 'posts', columnName: 'c', type: 'int' });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.patch).toHaveBeenCalled();
  });

  test('useDropColumn DELETEs', async () => {
    vi.mocked(api.delete).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useDropColumn('p1'), { wrapper: Wrapper });
    result.current.mutate({ tableName: 'posts', columnName: 'c' });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.delete).toHaveBeenCalled();
  });
});

describe('useSchemaObjects — extra', () => {
  test('useDropExtension', async () => {
    vi.mocked(api.delete).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useDropExtension('p1'), { wrapper: Wrapper });
    result.current.mutate({ name: 'pgvector', cascade: true });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.delete).toHaveBeenCalled();
  });

  test('usePolicies / useCreatePolicy / useDropPolicy', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [] } as never);
    vi.mocked(api.post).mockResolvedValueOnce({} as never);
    vi.mocked(api.delete).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();

    const { result: list } = renderHook(() => usePolicies('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(list.current.isSuccess).toBe(true));

    const { result: create } = renderHook(() => useCreatePolicy('p1'), { wrapper: Wrapper });
    create.current.mutate({ table: 'posts', name: 'p1', command: 'SELECT', roles: 'public' });
    await waitFor(() => expect(create.current.isSuccess).toBe(true));

    const { result: drop } = renderHook(() => useDropPolicy('p1'), { wrapper: Wrapper });
    drop.current.mutate({ name: 'p1', table: 'posts' });
    await waitFor(() => expect(drop.current.isSuccess).toBe(true));
  });

  test('useFunctions / useCreateFunction / useDropFunction', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [] } as never);
    vi.mocked(api.post).mockResolvedValueOnce({} as never);
    vi.mocked(api.delete).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();

    const { result: list } = renderHook(() => useFunctions('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(list.current.isSuccess).toBe(true));

    const { result: create } = renderHook(() => useCreateFunction('p1'), { wrapper: Wrapper });
    create.current.mutate({ name: 'f', body: 'SELECT 1', returnType: 'int', language: 'sql' });
    await waitFor(() => expect(create.current.isSuccess).toBe(true));

    const { result: drop } = renderHook(() => useDropFunction('p1'), { wrapper: Wrapper });
    drop.current.mutate({ name: 'f', argTypes: 'int' });
    await waitFor(() => expect(drop.current.isSuccess).toBe(true));
  });

  test('useTriggers / useCreateTrigger / useDropTrigger', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [] } as never);
    vi.mocked(api.post).mockResolvedValueOnce({} as never);
    vi.mocked(api.delete).mockResolvedValueOnce({} as never);
    const { Wrapper } = makeWrapper();

    const { result: list } = renderHook(() => useTriggers('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(list.current.isSuccess).toBe(true));

    const { result: create } = renderHook(() => useCreateTrigger('p1'), { wrapper: Wrapper });
    create.current.mutate({ name: 't', table: 'posts', timing: 'BEFORE', event: 'INSERT', function: 'f' });
    await waitFor(() => expect(create.current.isSuccess).toBe(true));

    const { result: drop } = renderHook(() => useDropTrigger('p1'), { wrapper: Wrapper });
    drop.current.mutate({ name: 't', table: 'posts' });
    await waitFor(() => expect(drop.current.isSuccess).toBe(true));
  });

  test('useIndexes GETs per-table indexes', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [] } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useIndexes('p1', 'posts'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/schema/p1/tables/posts/indexes', { params: { schema: 'public' } });
  });
});

describe('useSchemaAdvisors', () => {
  test('usePerformanceAdvisor', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [] } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => usePerformanceAdvisor('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/schema/p1/advisors/performance', { params: { schema: 'public' } });
  });

  test('useSecurityAdvisor', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: [] } as never);
    const { Wrapper } = makeWrapper();
    const { result } = renderHook(() => useSecurityAdvisor('p1'), { wrapper: Wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(api.get).toHaveBeenCalledWith('/schema/p1/advisors/security', { params: { schema: 'public' } });
  });
});
