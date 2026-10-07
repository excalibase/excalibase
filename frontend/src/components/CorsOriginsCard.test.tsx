import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { CorsOriginsCard } from './CorsOriginsCard';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() },
}));

const listed = (allowedOrigins: string[]) => ({
  data: { allowedOrigins, allowWildcard: allowedOrigins.length === 1 && allowedOrigins[0] === '*' },
});

function renderCard(origins: string[] | Error) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url !== '/projects/p-1/cors') return Promise.reject(new Error(`unexpected GET ${url}`));
    return origins instanceof Error ? Promise.reject(origins) : Promise.resolve(listed(origins) as never);
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <CorsOriginsCard projectId="p-1" />
    </QueryClientProvider>,
  );
}

const refusal = (status: number, error: string) => Object.assign(new Error('refused'), { response: { status, data: { error } } });

describe('CorsOriginsCard', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    vi.mocked(api.post).mockReset();
    vi.mocked(api.put).mockReset();
    vi.mocked(api.delete).mockReset();
  });

  test('lists the allowed origins and explains local dev, native apps and the propagation delay', async () => {
    renderCard(['http://localhost:5173', 'https://shop.example.com']);
    const list = await screen.findByTestId('cors-origins-list');
    expect(within(list).getByText('http://localhost:5173')).toBeInTheDocument();
    expect(within(list).getByText('https://shop.example.com')).toBeInTheDocument();
    const card = screen.getByTestId('cors-origins-card');
    expect(card).toHaveTextContent('Allowed origins');
    expect(card).toHaveTextContent('http://localhost:<port>');
    expect(card).toHaveTextContent('capacitor://localhost');
    expect(card).toHaveTextContent('tauri://localhost');
    expect(card).toHaveTextContent(/about a minute/);
  });

  test('says when no origin is allowed yet', async () => {
    renderCard([]);
    expect(await screen.findByTestId('cors-origins-empty')).toHaveTextContent(/no browser origin/i);
  });

  test('adds an origin through the one-origin endpoint and shows the new list', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { ...listed(['http://localhost:5173']).data, added: true } } as never);
    renderCard([]);
    await userEvent.type(await screen.findByLabelText('Origin'), ' http://localhost:5173 ');
    await userEvent.click(screen.getByRole('button', { name: 'Add origin' }));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/projects/p-1/cors/origins', { origin: 'http://localhost:5173' }),
    );
    expect(await within(screen.getByTestId('cors-origins-list')).findByText('http://localhost:5173')).toBeInTheDocument();
    expect(screen.getByLabelText('Origin')).toHaveValue('');
  });

  test('an empty origin is not sent', async () => {
    renderCard([]);
    await screen.findByTestId('cors-origins-empty');
    expect(screen.getByRole('button', { name: 'Add origin' })).toBeDisabled();
  });

  test('shows the server reason when an origin is refused', async () => {
    vi.mocked(api.post).mockRejectedValue(refusal(400, 'origin must be one origin, e.g. http://localhost:5173'));
    renderCard([]);
    await userEvent.type(await screen.findByLabelText('Origin'), 'localhost:5173/app');
    await userEvent.click(screen.getByRole('button', { name: 'Add origin' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('origin must be one origin');
  });

  test('removes an origin', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: { ...listed([]).data, removed: true } } as never);
    renderCard(['http://localhost:5173']);
    await userEvent.click(await screen.findByRole('button', { name: 'Remove http://localhost:5173' }));
    await waitFor(() =>
      expect(api.delete).toHaveBeenCalledWith('/projects/p-1/cors/origins', { params: { origin: 'http://localhost:5173' } }),
    );
    expect(await screen.findByTestId('cors-origins-empty')).toBeInTheDocument();
  });

  test('a project open to every origin says so and can be closed', async () => {
    vi.mocked(api.put).mockResolvedValue(listed([]) as never);
    renderCard(['*']);
    expect(await screen.findByTestId('cors-wildcard')).toHaveTextContent(/every origin/i);
    await userEvent.click(screen.getByRole('button', { name: 'Stop allowing every origin' }));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/projects/p-1/cors', { allowedOrigins: [], allowWildcard: false }),
    );
    expect(await screen.findByTestId('cors-origins-empty')).toBeInTheDocument();
  });

  test('a read the server refuses shows its reason', async () => {
    renderCard(refusal(403, 'requires the developer role'));
    expect(await screen.findByTestId('cors-origins-error')).toHaveTextContent('requires the developer role');
  });
});
