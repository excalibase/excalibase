import { describe, test, expect, vi, beforeEach } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { StoragePage } from './StoragePage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const BUCKETS = '/projects/p-1/storage/buckets';

function refusal(status: number, error: string) {
  return { message: `Request failed with status code ${status}`, response: { status, data: { error, status } } };
}

function renderPage(buckets: unknown = []) {
  vi.mocked(api.get).mockImplementation(async (url: string) => {
    if (url === BUCKETS) {
      if (buckets instanceof Error || (buckets as { response?: unknown }).response) throw buckets;
      return { data: buckets } as never;
    }
    return { data: { objects: [], prefixes: [] } } as never;
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/p-1/storage']}>
        <Routes>
          <Route path="/project/:projectId/storage" element={<StoragePage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function openCreate() {
  await userEvent.click(await screen.findByTitle('Create bucket'));
  return screen.getByPlaceholderText('avatars');
}

describe('Storage — create bucket', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.spyOn(window, 'alert').mockImplementation(() => {});
  });

  test('a name the server would refuse is explained next to the field and never sent', async () => {
    renderPage();
    await userEvent.type(await openCreate(), 'My Bucket!');
    await userEvent.click(screen.getByRole('button', { name: 'Create' }));
    expect(screen.getByTestId('bucket-name-error')).toHaveTextContent(/lowercase letters, digits and hyphens/);
    expect(api.post).not.toHaveBeenCalled();
  });

  test("a refusal shows the server's reason in the form, not an alert", async () => {
    renderPage();
    vi.mocked(api.post).mockRejectedValue(refusal(409, 'bucket already exists'));
    await userEvent.type(await openCreate(), 'avatars');
    await userEvent.click(screen.getByRole('button', { name: 'Create' }));
    expect(await screen.findByTestId('bucket-name-error')).toHaveTextContent('bucket already exists');
    expect(window.alert).not.toHaveBeenCalled();
  });

  test('a double click creates the bucket once', async () => {
    renderPage();
    let finish: (value: unknown) => void = () => {};
    vi.mocked(api.post).mockImplementation(() => new Promise((resolve) => { finish = resolve; }) as never);
    await userEvent.type(await openCreate(), 'avatars');
    const create = screen.getByRole('button', { name: 'Create' });
    fireEvent.click(create);
    fireEvent.click(create);
    finish({ data: { id: 'b1', name: 'avatars', public: false } });
    await waitFor(() => expect(api.post).toHaveBeenCalledTimes(1));
  });
});

describe('Storage — bucket list and delete', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
  });

  test('a list that failed to load says so instead of "No buckets yet"', async () => {
    renderPage(refusal(503, 'storage is not configured on this platform'));
    expect(await screen.findByTestId('buckets-error')).toHaveTextContent('storage is not configured on this platform');
    expect(screen.queryByText(/No buckets yet/)).not.toBeInTheDocument();
  });

  test('a refused delete shows the reason', async () => {
    renderPage([{ id: 'b1', name: 'avatars', public: false }]);
    vi.mocked(api.delete).mockRejectedValue(refusal(409, 'bucket is being deleted'));
    await userEvent.click(await screen.findByRole('button', { name: 'Delete bucket' }));
    expect(await screen.findByTestId('bucket-delete-error')).toHaveTextContent('bucket is being deleted');
  });
});
