import { describe, test, expect, vi, beforeEach } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { CollectionSidebar } from './CollectionSidebar';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const conflict = (reason: string) => ({
  message: 'Request failed with status code 409',
  response: { status: 409, data: { error: reason, status: 409 } },
});

const COLLECTIONS_URL = '/projects/proj-1/documentdb/databases/shop/collections';

function mount(props: { database?: string; collection?: string; databases?: string[] } = {}) {
  const onDatabase = vi.fn();
  const onCollection = vi.fn();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <CollectionSidebar
        projectId="proj-1"
        databases={props.databases ?? ['shop']}
        database={props.database ?? 'shop'}
        collection={props.collection ?? ''}
        onDatabase={onDatabase}
        onCollection={onCollection}
      />
    </QueryClientProvider>,
  );
  return { onDatabase, onCollection };
}

const typeCollection = (name: string) =>
  fireEvent.change(screen.getByLabelText('New collection name'), { target: { value: name } });

describe('CollectionSidebar', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({ data: { collections: [{ name: 'orders', type: 'collection' }] } } as never);
  });

  test('lists the collections and opens one on click', async () => {
    const { onCollection } = mount();
    fireEvent.click(await screen.findByRole('button', { name: 'orders' }));
    expect(onCollection).toHaveBeenCalledWith('orders');
  });

  test('a blank collection name is not sent', () => {
    mount();
    typeCollection('   ');
    fireEvent.click(screen.getByLabelText('Create collection'));
    expect(api.post).not.toHaveBeenCalled();
  });

  test('creates a collection and opens it', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
    const { onCollection } = mount();
    typeCollection(' carts ');
    fireEvent.click(screen.getByLabelText('Create collection'));
    await vi.waitFor(() => expect(onCollection).toHaveBeenCalledWith('carts'));
    expect(api.post).toHaveBeenCalledWith(COLLECTIONS_URL, { name: 'carts' });
  });

  test('a refused create shows the server reason, not the status code', async () => {
    vi.mocked(api.post).mockRejectedValue(conflict('collection carts already exists'));
    mount();
    typeCollection('carts');
    fireEvent.click(screen.getByLabelText('Create collection'));
    expect(await screen.findByRole('alert')).toHaveTextContent('collection carts already exists');
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('two quick submits create the collection once', async () => {
    vi.mocked(api.post).mockReturnValue(new Promise(() => {}));
    mount();
    typeCollection('carts');
    fireEvent.click(screen.getByLabelText('Create collection'));
    fireEvent.click(screen.getByLabelText('Create collection'));
    await vi.waitFor(() => expect(api.post).toHaveBeenCalled());
    expect(api.post).toHaveBeenCalledTimes(1);
  });

  test('a new database name is used once typed, and blank is ignored', () => {
    const { onDatabase } = mount();
    fireEvent.click(screen.getByLabelText('Use new database'));
    expect(onDatabase).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText('New database name'), { target: { value: ' logs ' } });
    fireEvent.click(screen.getByLabelText('Use new database'));
    expect(onDatabase).toHaveBeenCalledWith('logs');
  });

  test('a chosen database not yet listed is still offered, and switching picks another', () => {
    const { onDatabase } = mount({ database: 'logs' });
    expect(screen.getByRole('option', { name: 'logs' })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Database'), { target: { value: 'shop' } });
    expect(onDatabase).toHaveBeenCalledWith('shop');
  });

  test('with no databases it says so and offers no collection form', () => {
    mount({ database: '', databases: [] });
    expect(screen.getByRole('option', { name: 'No databases yet' })).toBeInTheDocument();
    expect(screen.queryByLabelText('New collection name')).not.toBeInTheDocument();
  });

  test('dropping the open collection closes it', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: {} } as never);
    const { onCollection } = mount({ collection: 'orders' });
    fireEvent.click(await screen.findByLabelText('Drop collection orders'));
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    await vi.waitFor(() => expect(onCollection).toHaveBeenCalledWith(''));
    expect(api.delete).toHaveBeenCalledWith(`${COLLECTIONS_URL}/orders/`);
  });

  test('a refused drop shows the server reason', async () => {
    vi.mocked(api.delete).mockRejectedValue(conflict('orders is referenced by a view'));
    mount();
    fireEvent.click(await screen.findByLabelText('Drop collection orders'));
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('orders is referenced by a view');
  });

  test('a failed listing shows why', async () => {
    vi.mocked(api.get).mockRejectedValue(conflict('documentdb is starting'));
    mount();
    expect(await screen.findByRole('alert')).toHaveTextContent('documentdb is starting');
  });
});
