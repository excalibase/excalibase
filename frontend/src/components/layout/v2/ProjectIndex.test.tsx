import { describe, test, expect, vi, beforeEach } from 'vitest';
import { Suspense } from 'react';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { ProjectIndex } from './ProjectIndex';
import { api } from '../../../api/client';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn() } }));
vi.mock('../../../pages/ProjectOverviewPage', () => ({ ProjectOverviewPage: () => <div>old overview</div> }));
vi.mock('../../../pages/ProjectServicesPage', () => ({ ProjectServicesPage: () => <div>services view</div> }));

function mount(config: Promise<unknown>) {
  vi.mocked(api.get).mockImplementation((() => config) as never);
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter><Suspense fallback={<p>suspended</p>}><ProjectIndex /></Suspense></MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => vi.clearAllMocks());

describe('ProjectIndex', () => {
  test('flag off shows the old overview', async () => {
    mount(Promise.resolve({ data: { deploymentMode: 'cloud', features: { studio_v2: false } } }));
    expect(await screen.findByText('old overview')).toBeInTheDocument();
    expect(screen.queryByText('services view')).not.toBeInTheDocument();
  });

  test('flag on shows the services view', async () => {
    mount(Promise.resolve({ data: { deploymentMode: 'cloud', features: { studio_v2: true } } }));
    expect(await screen.findByText('services view')).toBeInTheDocument();
  });

  test('while the flag is loading it shows a spinner, not the old overview', async () => {
    mount(new Promise(() => {}));
    expect(await screen.findByRole('status', { name: /loading/i })).toBeInTheDocument();
    expect(screen.queryByText('old overview')).not.toBeInTheDocument();
  });

  test('a config request that failed once is not waited for again when the page mounts', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = () => (
      <QueryClientProvider client={client}>
        <MemoryRouter><Suspense fallback={<p>suspended</p>}><ProjectIndex /></Suspense></MemoryRouter>
      </QueryClientProvider>
    );
    vi.mocked(api.get).mockRejectedValueOnce(new Error('down'));
    const first = render(view());
    expect(await screen.findByText('old overview')).toBeInTheDocument();
    first.unmount();
    vi.mocked(api.get).mockImplementation((() => new Promise(() => {})) as never);
    render(view());
    expect(await screen.findByText('old overview')).toBeInTheDocument();
    expect(screen.queryByRole('status', { name: /loading/i })).not.toBeInTheDocument();
  });
});
