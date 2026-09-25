import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { DocumentsPage } from './DocumentsPage';
import { SubNav } from '../components/layout/SubNav';
import { api } from '../api/client';
import { editorText, typeIntoEditor } from '../test/codemirror';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() },
}));

const coll = '/projects/proj-doc/documentdb/databases/shop/collections/orders';

interface State {
  documentDb: boolean;
  documents: Array<Record<string, unknown>>;
  count: number;
  indexes: Array<Record<string, unknown>>;
}

function mockServer(state: State) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    const reply = (data: unknown) => Promise.resolve({ data } as never);
    if (url === '/provision/proj-doc') return reply({ projectId: 'proj-doc', documentDb: state.documentDb });
    if (url === '/projects/proj-doc/documentdb/databases') return reply({ databases: ['shop', 'crm'] });
    if (url === '/projects/proj-doc/documentdb/databases/shop/collections') {
      return reply({ collections: [{ name: 'orders', type: 'collection' }] });
    }
    if (url === `${coll}/documents`) return reply({ documents: state.documents, limit: 20, skip: 0 });
    if (url === `${coll}/count`) return reply({ count: state.count });
    if (url === `${coll}/sample`) return reply({ documents: state.documents });
    if (url === `${coll}/indexes`) return reply({ indexes: state.indexes });
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  vi.mocked(api.post).mockResolvedValue({ data: { insertedId: { $oid: 'new' }, name: 'total_1' } } as never);
  vi.mocked(api.put).mockResolvedValue({ data: null } as never);
  vi.mocked(api.delete).mockResolvedValue({ data: null } as never);
}

function renderAt(element: React.ReactElement, path = '/project/proj-doc/database/documents') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/project/:projectId/database/documents" element={element} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const twoOrders = [
  { _id: { $oid: '65f000000000000000000001' }, total: { $numberDouble: '5.0' }, customer: { name: 'a' } },
  { _id: { $numberInt: '2' }, total: { $numberInt: '9' } },
];

async function openOrders() {
  renderAt(<DocumentsPage />);
  await userEvent.click(await screen.findByRole('button', { name: 'orders' }));
  await screen.findAllByTestId('document-row');
}

function getCalls(url: string) {
  return vi.mocked(api.get).mock.calls.filter(([called]) => called === url);
}

describe('DocumentsPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockServer({ documentDb: true, documents: twoOrders, count: 2, indexes: [{ name: '_id_', key: { _id: 1 } }] });
  });

  test('a project without DocumentDB is told so and nothing is asked of the gateway', async () => {
    mockServer({ documentDb: false, documents: [], count: 0, indexes: [] });
    renderAt(<DocumentsPage />);
    expect(await screen.findByTestId('documents-unavailable')).toBeInTheDocument();
    expect(vi.mocked(api.get).mock.calls.some(([url]) => String(url).includes('/documentdb'))).toBe(false);
  });

  test('lists databases, then collections, then documents with the total', async () => {
    await openOrders();
    expect(screen.getByRole('combobox', { name: 'Database' })).toHaveValue('shop');
    const rows = screen.getAllByTestId('document-row');
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent('"$oid": "65f000000000000000000001"');
    expect(rows[0]).toHaveTextContent('"$numberDouble": "5.0"');
    expect(rows[1]).toHaveTextContent('"total": 9');
    expect(screen.getByTestId('page-range')).toHaveTextContent('1–2 of 2');
  });

  test('a filter typed in shell syntax is sent as Extended JSON', async () => {
    await openOrders();
    typeIntoEditor(screen.getByTestId('query-filter'), '{total: {$gt: 5}}');
    typeIntoEditor(screen.getByTestId('query-sort'), '{total: -1}');
    await userEvent.click(screen.getByRole('button', { name: /find/i }));

    await waitFor(() => {
      const withFilter = getCalls(`${coll}/documents`).find(([, config]) => (config as { params?: { filter?: string } })?.params?.filter);
      expect(withFilter).toBeDefined();
      const params = (withFilter![1] as { params: Record<string, unknown> }).params;
      expect(JSON.parse(params.filter as string)).toEqual({ total: { $gt: { $numberInt: '5' } } });
      expect(JSON.parse(params.sort as string)).toEqual({ total: { $numberInt: '-1' } });
      expect(params.skip).toBe(0);
    });
  });

  test('a filter that does not parse is refused in the page', async () => {
    await openOrders();
    const before = getCalls(`${coll}/documents`).length;
    typeIntoEditor(screen.getByTestId('query-filter'), '{total: ');
    await userEvent.click(screen.getByRole('button', { name: /find/i }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/filter does not parse/i);
    expect(getCalls(`${coll}/documents`)).toHaveLength(before);
  });

  test('the server refusing a query is shown', async () => {
    await openOrders();
    vi.mocked(api.get).mockImplementation((url: string) => {
      if (url === `${coll}/documents`) {
        return Promise.reject({ response: { data: { error: 'unknown operator: $gtx' } } });
      }
      return Promise.resolve({ data: { count: 0, documents: [] } } as never);
    });
    typeIntoEditor(screen.getByTestId('query-filter'), '{a: {$gtx: 1}}');
    await userEvent.click(screen.getByRole('button', { name: /find/i }));
    expect(await screen.findByText('unknown operator: $gtx')).toBeInTheDocument();
  });

  test('inserting validates the JSON before sending it', async () => {
    await openOrders();
    await userEvent.click(screen.getByRole('button', { name: /insert document/i }));
    const editor = screen.getByTestId('document-editor');

    typeIntoEditor(within(editor).getByTestId('document-json'), '{"total": ');
    expect(within(editor).getByRole('alert')).toHaveTextContent(/not valid json/i);
    expect(within(editor).getByRole('button', { name: 'Save' })).toBeDisabled();

    typeIntoEditor(within(editor).getByTestId('document-json'), '[1]');
    expect(within(editor).getByRole('alert')).toHaveTextContent(/must be a json object/i);

    typeIntoEditor(within(editor).getByTestId('document-json'), '{"total": 12}');
    await userEvent.click(within(editor).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith(`${coll}/documents`, '{"total": 12}', expect.anything()));
    await waitFor(() => expect(screen.queryByTestId('document-editor')).not.toBeInTheDocument());
  });

  test('a refused insert keeps the editor open with the reason', async () => {
    await openOrders();
    vi.mocked(api.post).mockRejectedValueOnce({ response: { data: { error: 'E11000 duplicate key' } } });
    await userEvent.click(screen.getByRole('button', { name: /insert document/i }));
    typeIntoEditor(screen.getByTestId('document-json'), '{"_id": 2}');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('E11000 duplicate key')).toBeInTheDocument();
    expect(screen.getByTestId('document-editor')).toBeInTheDocument();
  });

  test('editing replaces the document by its _id', async () => {
    await openOrders();
    const [first] = screen.getAllByTestId('document-row');
    await userEvent.click(within(first).getByRole('button', { name: 'Edit document' }));
    expect(editorText(screen.getByTestId('document-json'))).toBe(
      JSON.stringify({ _id: { $oid: '65f000000000000000000001' }, total: { $numberDouble: '5.0' }, customer: { name: 'a' } }, null, 2),
    );
    typeIntoEditor(screen.getByTestId('document-json'), '{"_id": {"$oid": "65f000000000000000000001"}, "total": 6}');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        `${coll}/documents`,
        '{"_id": {"$oid": "65f000000000000000000001"}, "total": 6}',
        expect.objectContaining({ params: { id: '{"$oid":"65f000000000000000000001"}' } }),
      ),
    );
  });

  test('deleting asks in the page first', async () => {
    await openOrders();
    const second = screen.getAllByTestId('document-row')[1];
    await userEvent.click(within(second).getByRole('button', { name: 'Delete document' }));
    expect(within(second).getByText('Delete this document?')).toBeInTheDocument();
    await userEvent.click(within(second).getByRole('button', { name: 'Keep' }));
    expect(api.delete).not.toHaveBeenCalled();

    await userEvent.click(within(second).getByRole('button', { name: 'Delete document' }));
    await userEvent.click(within(second).getByRole('button', { name: 'Delete' }));
    await waitFor(() =>
      expect(api.delete).toHaveBeenCalledWith(`${coll}/documents`, { params: { id: '{"$numberInt":"2"}' } }),
    );
  });

  test('the next page skips what was shown', async () => {
    mockServer({ documentDb: true, documents: twoOrders, count: 45, indexes: [] });
    await openOrders();
    await userEvent.click(screen.getByRole('button', { name: 'Next page' }));
    await waitFor(() =>
      expect(getCalls(`${coll}/documents`).some(([, config]) => (config as { params: { skip: number } }).params.skip === 20)).toBe(true),
    );
    expect(screen.getByRole('button', { name: 'Previous page' })).toBeEnabled();
  });

  test('indexes are listed, created and dropped', async () => {
    mockServer({
      documentDb: true,
      documents: twoOrders,
      count: 2,
      indexes: [
        { name: '_id_', key: { _id: { $numberInt: '1' } } },
        { name: 'total_1', key: { total: { $numberInt: '1' } }, unique: true },
      ],
    });
    await openOrders();
    await userEvent.click(screen.getByRole('tab', { name: 'indexes' }));
    const rows = await screen.findAllByTestId('index-row');
    expect(rows).toHaveLength(2);
    expect(rows[1]).toHaveTextContent('total: 1');
    expect(within(rows[0]).queryByRole('button', { name: /drop index/i })).toBeNull();

    await userEvent.type(screen.getByLabelText('Index field'), 'customer.name');
    await userEvent.selectOptions(screen.getByLabelText('Index type'), '-1');
    await userEvent.click(screen.getByRole('button', { name: 'Create index' }));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(`${coll}/indexes`, { keys: { 'customer.name': -1 }, unique: false }),
    );

    await userEvent.click(within(rows[1]).getByRole('button', { name: 'Drop index total_1' }));
    await userEvent.click(within(rows[1]).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith(`${coll}/indexes/total_1`));
  });

  test('an index needs a field, and a refused one says why', async () => {
    await openOrders();
    await userEvent.click(screen.getByRole('tab', { name: 'indexes' }));
    await screen.findAllByTestId('index-row');
    await userEvent.click(screen.getByRole('button', { name: 'Create index' }));
    expect(screen.getByRole('alert')).toHaveTextContent('Name the field to index');
    expect(api.post).not.toHaveBeenCalled();

    vi.mocked(api.post).mockRejectedValueOnce({ response: { data: { error: 'Index already exists with a different name' } } });
    await userEvent.type(screen.getByLabelText('Index field'), 'total');
    await userEvent.selectOptions(screen.getByLabelText('Index type'), 'text');
    await userEvent.type(screen.getByLabelText('Index name'), 'by_total');
    await userEvent.click(screen.getByLabelText('Unique'));
    await userEvent.click(screen.getByRole('button', { name: 'Create index' }));
    expect(await screen.findByText('Index already exists with a different name')).toBeInTheDocument();
    expect(api.post).toHaveBeenCalledWith(`${coll}/indexes`, { keys: { total: 'text' }, unique: true, name: 'by_total' });
  });

  test('collections are created in the chosen database and dropped after asking', async () => {
    await openOrders();
    await userEvent.type(screen.getByLabelText('New collection name'), 'invoices');
    await userEvent.click(screen.getByRole('button', { name: 'Create collection' }));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/projects/proj-doc/documentdb/databases/shop/collections', { name: 'invoices' }),
    );

    await userEvent.click(screen.getByRole('button', { name: 'Drop collection orders' }));
    await userEvent.click(screen.getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith(`${coll}/`));
  });

  test('a new database can be named before it holds anything', async () => {
    await openOrders();
    await userEvent.type(screen.getByLabelText('New database name'), 'fresh');
    await userEvent.click(screen.getByRole('button', { name: 'Use new database' }));
    expect(screen.getByRole('combobox', { name: 'Database' })).toHaveValue('fresh');
    expect(screen.getByText('Choose a collection.')).toBeInTheDocument();
  });
});

describe('Database sub-navigation', () => {
  beforeEach(() => vi.clearAllMocks());

  test('shows Documents only for a DocumentDB project', async () => {
    mockServer({ documentDb: true, documents: [], count: 0, indexes: [] });
    const { unmount } = renderAt(<SubNav sectionKey="database" />);
    expect(await screen.findByRole('link', { name: /documents/i })).toBeInTheDocument();
    unmount();

    mockServer({ documentDb: false, documents: [], count: 0, indexes: [] });
    renderAt(<SubNav sectionKey="database" />);
    await screen.findByRole('link', { name: /tables/i });
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/provision/proj-doc'));
    expect(screen.queryByRole('link', { name: /documents/i })).toBeNull();
  });
});
