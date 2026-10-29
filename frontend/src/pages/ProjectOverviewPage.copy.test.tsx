import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { ProjectOverviewPage } from './ProjectOverviewPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn() } }));

// EXC-667: a copy says which database it was copied from.

const copy = {
  projectId: 'copy-1',
  projectName: 'Shop staging',
  orgId: 'org-1',
  databaseType: 'POSTGRESQL',
  tier: 'STANDARD',
  status: 'ACTIVE',
  currentStage: 'COMPLETED',
  namespace: 'org-1-copy-1',
  restoredFromProjectId: 'shop-1',
  restoredFromBackupId: 'backup-20261029',
};

function renderPage({ dbcopy, visible }: { dbcopy: boolean; visible: Record<string, unknown>[] }) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    const answer = (value: unknown) => Promise.resolve({ data: value } as never);
    if (url === '/config') return answer({ deploymentMode: 'cloud', appHosting: false, sites: true, features: { dbcopy } });
    if (url === '/provision/copy-1') return answer(copy);
    if (url === '/provision') return answer([copy, ...visible]);
    if (url === '/projects/copy-1/db-endpoint') return answer({ publicEnabled: false, available: false, host: '', port: 0, internal: {} });
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/copy-1']}>
        <Routes>
          <Route path="/project/:projectId" element={<ProjectOverviewPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('ProjectOverviewPage on a copy', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
  });

  test('names and links the source the caller can see', async () => {
    renderPage({ dbcopy: true, visible: [{ projectId: 'shop-1', projectName: 'Shop' }] });
    expect(await screen.findByRole('link', { name: 'Shop' })).toHaveAttribute('href', '/project/shop-1');
    const note = screen.getByTestId('copied-from');
    expect(note).toHaveTextContent('Copy of Shop');
    expect(note).toHaveTextContent('backup-20261029');
  });

  test('a source the caller cannot see, or that is gone, is named by id without a link', async () => {
    renderPage({ dbcopy: true, visible: [] });
    const note = await screen.findByTestId('copied-from');
    expect(note).toHaveTextContent('Copy of shop-1');
    expect(screen.queryByRole('link', { name: 'shop-1' })).not.toBeInTheDocument();
  });

  test('says nothing while the feature is off', async () => {
    renderPage({ dbcopy: false, visible: [{ projectId: 'shop-1', projectName: 'Shop' }] });
    expect(await screen.findByRole('heading', { name: 'Shop staging' })).toBeInTheDocument();
    expect(screen.queryByTestId('copied-from')).not.toBeInTheDocument();
  });
});
