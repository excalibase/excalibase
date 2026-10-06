import { describe, test, expect, vi, beforeEach } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { IndexesPanel } from './IndexesPanel';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const conflict = (reason: string) => ({
  message: 'Request failed with status code 409',
  response: { status: 409, data: { error: reason, status: 409 } },
});

const INDEXES_URL = '/projects/proj-1/documentdb/databases/shop/collections/users/indexes';

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <IndexesPanel collectionRef={{ projectId: 'proj-1', database: 'shop', collection: 'users' }} />
    </QueryClientProvider>,
  );
}

const submit = () => fireEvent.click(screen.getByRole('button', { name: 'Create index' }));

describe('IndexesPanel', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({
      data: {
        indexes: [
          { name: '_id_', key: { _id: 1 } },
          { name: 'email_1', key: { email: 1 }, unique: true },
        ],
      },
    } as never);
  });

  test('lists indexes, and the _id index cannot be dropped', async () => {
    mount();
    expect(await screen.findAllByTestId('index-row')).toHaveLength(2);
    expect(screen.getByText('email: 1')).toBeInTheDocument();
    expect(screen.queryByLabelText('Drop index _id_')).not.toBeInTheDocument();
    expect(screen.getByLabelText('Drop index email_1')).toBeInTheDocument();
  });

  test('an empty field is refused before any request', () => {
    mount();
    submit();
    expect(screen.getByRole('alert')).toHaveTextContent('Name the field to index');
    expect(api.post).not.toHaveBeenCalled();
  });

  test('creates a unique descending index with a name', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { name: 'created_desc' } } as never);
    mount();
    fireEvent.change(screen.getByLabelText('Index field'), { target: { value: ' createdAt ' } });
    fireEvent.change(screen.getByLabelText('Index type'), { target: { value: '-1' } });
    fireEvent.change(screen.getByLabelText('Index name'), { target: { value: 'created_desc' } });
    fireEvent.click(screen.getByLabelText('Unique'));
    submit();
    await vi.waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(INDEXES_URL, { keys: { createdAt: -1 }, unique: true, name: 'created_desc' }),
    );
    await vi.waitFor(() => expect(screen.getByLabelText('Index field')).toHaveValue(''));
  });

  test('a text index sends its type as the key value', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { name: 'bio_text' } } as never);
    mount();
    fireEvent.change(screen.getByLabelText('Index field'), { target: { value: 'bio' } });
    fireEvent.change(screen.getByLabelText('Index type'), { target: { value: 'text' } });
    submit();
    await vi.waitFor(() => expect(api.post).toHaveBeenCalledWith(INDEXES_URL, { keys: { bio: 'text' }, unique: false }));
  });

  test('a refused create shows the server reason, not the status code', async () => {
    vi.mocked(api.post).mockRejectedValue(conflict('an index named email_1 already exists'));
    mount();
    fireEvent.change(screen.getByLabelText('Index field'), { target: { value: 'email' } });
    submit();
    expect(await screen.findByRole('alert')).toHaveTextContent('an index named email_1 already exists');
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('two quick submits create the index once', async () => {
    vi.mocked(api.post).mockReturnValue(new Promise(() => {}));
    mount();
    fireEvent.change(screen.getByLabelText('Index field'), { target: { value: 'email' } });
    submit();
    submit();
    await vi.waitFor(() => expect(api.post).toHaveBeenCalled());
    expect(api.post).toHaveBeenCalledTimes(1);
  });

  test('a refused drop shows the server reason', async () => {
    vi.mocked(api.delete).mockRejectedValue(conflict('email_1 backs a unique constraint'));
    mount();
    fireEvent.click(await screen.findByLabelText('Drop index email_1'));
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('email_1 backs a unique constraint');
    expect(api.delete).toHaveBeenCalledWith(`${INDEXES_URL}/email_1`);
  });

  test('a failed listing shows why', async () => {
    vi.mocked(api.get).mockRejectedValue(conflict('documentdb is starting'));
    mount();
    expect(await screen.findByRole('alert')).toHaveTextContent('documentdb is starting');
  });
});
