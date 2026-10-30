import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { SitePreviews } from './SitePreviews';
import { api } from '../../api/client';
import type { PreviewOverview } from '../../api/sitePreviews';

vi.mock('../../api/client', () => ({
  api: { get: vi.fn(), put: vi.fn(), delete: vi.fn() },
}));

const BASE = '/projects/proj-1/sites/site-1/previews';

const overview = (overrides: Partial<PreviewOverview> = {}): PreviewOverview => ({
  settings: { mode: 'branches', forks: false, secrets: false, apiAccess: false, publicLinks: false },
  previews: [
    {
      id: 'pv-1',
      siteId: 'site-1',
      source: 'branch',
      ref: 'feat/header',
      hostname: 'shop--abcdefghijklmnopqrstuvwxyz.excalibase.app',
      status: 'READY',
      commitSha: '2222222222222222222222222222222222222222',
      deployedSha: '2222222222222222222222222222222222222222',
      fileCount: 3,
      totalBytes: 2048,
      createdAt: '2026-10-30T10:00:00Z',
      updatedAt: '2026-10-30T10:05:00Z',
    },
  ],
  used: 1,
  max: 2,
  ...overrides,
});

function renderPreviews({
  featureOn = true,
  canDevelop = true,
  canAdmin = false,
  data = overview(),
}: { featureOn?: boolean; canDevelop?: boolean; canAdmin?: boolean; data?: PreviewOverview } = {}) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config')
      return Promise.resolve({ data: { deploymentMode: 'cloud', features: { sitepreviews: featureOn } } } as never);
    if (url === BASE) return Promise.resolve({ data } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SitePreviews projectId="proj-1" siteId="site-1" productionBranch="main" canDevelop={canDevelop} canAdmin={canAdmin} />
    </QueryClientProvider>,
  );
}

beforeEach(() => vi.resetAllMocks());

// EXC-660: a preview per branch or pull request, at its own unguessable link.
describe('SitePreviews', () => {
  test('is hidden while the feature is off and asks nothing of the previews API', async () => {
    renderPreviews({ featureOn: false });
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/config'));
    expect(screen.queryByTestId('site-previews')).not.toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalledWith(BASE);
  });

  test('lists each preview with its link, status and the plan use', async () => {
    renderPreviews();
    const link = await screen.findByTestId('preview-link-pv-1');
    expect(link).toHaveAttribute('href', 'https://shop--abcdefghijklmnopqrstuvwxyz.excalibase.app');
    expect(link).toHaveAttribute('rel', expect.stringContaining('noreferrer'));
    expect(screen.getByTestId('preview-pv-1')).toHaveTextContent('feat/header');
    expect(screen.getByTestId('preview-pv-1')).toHaveTextContent('Ready');
    expect(screen.getByTestId('previews-usage')).toHaveTextContent('1 of 2');
  });

  test('a developer sets the mode, naming the production branch it skips', async () => {
    renderPreviews();
    vi.mocked(api.put).mockResolvedValue({ data: overview({ settings: { ...overview().settings, mode: 'pull-requests' } }) } as never);
    const select = await screen.findByTestId('preview-mode');
    expect(screen.getByRole('option', { name: /except main/ })).toBeInTheDocument();
    await userEvent.selectOptions(select, 'pull-requests');
    await waitFor(() => expect(api.put).toHaveBeenCalledWith(`${BASE}/mode`, { mode: 'pull-requests' }));
  });

  test('turning previews off warns that they are removed', async () => {
    renderPreviews();
    await userEvent.selectOptions(await screen.findByTestId('preview-mode'), 'off');
    expect(api.put).not.toHaveBeenCalled();
    expect(screen.getByTestId('preview-off-confirm')).toHaveTextContent('removes');
    vi.mocked(api.put).mockResolvedValue({ data: overview({ previews: [] }) } as never);
    await userEvent.click(screen.getByTestId('preview-off-confirm-button'));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith(`${BASE}/mode`, { mode: 'off' }));
  });

  test('the opt-ins are read-only below Admin', async () => {
    renderPreviews({ canAdmin: false });
    expect(await screen.findByTestId('preview-access-secrets')).toBeDisabled();
    expect(screen.getByTestId('preview-access-note')).toHaveTextContent('Admin');
  });

  test('an Admin turns on an opt-in after reading its warning', async () => {
    renderPreviews({ canAdmin: true });
    vi.mocked(api.put).mockResolvedValue({ data: overview() } as never);
    const forks = await screen.findByTestId('preview-access-forks');
    expect(screen.getByTestId('preview-access-forks-warning')).toHaveTextContent('never');
    await userEvent.click(forks);
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(`${BASE}/access`, {
        forks: true,
        secrets: false,
        apiAccess: false,
        publicLinks: false,
      }),
    );
  });

  test('a developer removes a preview; a viewer cannot', async () => {
    renderPreviews();
    vi.mocked(api.delete).mockResolvedValue({ data: undefined } as never);
    await userEvent.click(await screen.findByTestId('preview-remove-pv-1'));
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith(`${BASE}/pv-1`));
  });

  test('a viewer sees the previews and no control', async () => {
    renderPreviews({ canDevelop: false });
    await screen.findByTestId('preview-pv-1');
    expect(screen.queryByTestId('preview-remove-pv-1')).not.toBeInTheDocument();
    expect(screen.getByTestId('preview-mode')).toBeDisabled();
  });

  test('a failed preview shows why', async () => {
    const failed = overview();
    failed.previews[0] = { ...failed.previews[0], status: 'FAILED', failureReason: 'the build command failed (exit 1); see the log' };
    renderPreviews({ data: failed });
    expect(await screen.findByTestId('preview-pv-1')).toHaveTextContent('the build command failed');
  });
});
