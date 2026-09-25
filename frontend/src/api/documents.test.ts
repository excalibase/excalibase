import { describe, test, expect, vi, beforeEach } from 'vitest';
import { api } from './client';
import { apiErrorMessage, countDocuments, dropIndex, findDocuments, listCollections } from './documents';

vi.mock('./client', () => ({ api: { get: vi.fn(), delete: vi.fn() } }));

describe('documents api', () => {
  beforeEach(() => vi.clearAllMocks());

  test('names are escaped into the path', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { documents: [], limit: 20, skip: 0, collections: [] } } as never);
    await findDocuments({ projectId: 'p', database: 'shop', collection: 'a/b c' }, { filter: '{}' }, { limit: 20, skip: 40 });
    expect(api.get).toHaveBeenCalledWith('/projects/p/documentdb/databases/shop/collections/a%2Fb%20c/documents', {
      params: { filter: '{}', limit: 20, skip: 40 },
    });
    await listCollections('p', 'my db');
    expect(api.get).toHaveBeenLastCalledWith('/projects/p/documentdb/databases/my%20db/collections');
  });

  test('a count without a filter sends none', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { count: 3 } } as never);
    expect(await countDocuments({ projectId: 'p', database: 'd', collection: 'c' })).toBe(3);
    expect(api.get).toHaveBeenCalledWith('/projects/p/documentdb/databases/d/collections/c/count', { params: {} });
  });

  test('index names are escaped', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: null } as never);
    await dropIndex({ projectId: 'p', database: 'd', collection: 'c' }, 'a b');
    expect(api.delete).toHaveBeenCalledWith('/projects/p/documentdb/databases/d/collections/c/indexes/a%20b');
  });

  test('errors read the server message first', () => {
    expect(apiErrorMessage({ response: { data: { error: 'bad filter' } } })).toBe('bad filter');
    expect(apiErrorMessage(new Error('Network Error'))).toBe('Network Error');
    expect(apiErrorMessage(undefined)).toBe('Request failed');
  });
});
