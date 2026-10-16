import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SiteDetailPage } from './SiteDetailPage';
import { api } from '../api/client';
import type { Site, SiteDeploy } from '../api/sites';
import {
  developerRole,
  signInAsMember,
  siteDeployFixture,
  siteFixture,
} from '../test/siteFixtures';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

const live = siteDeployFixture({
  id: 'dep-2',
  createdAt: '2026-10-12T10:00:00Z',
  live: true,
  label: 'abc1234',
  totalBytes: 2 * 1024 * 1024,
});
const older = siteDeployFixture({ id: 'dep-1', createdAt: '2026-10-11T10:00:00Z' });
const failed = siteDeployFixture({
  id: 'dep-3',
  createdAt: '2026-10-13T10:00:00Z',
  status: 'FAILED',
  failureReason: 'the CDN refused the upload',
});

function renderPage({
  site = siteFixture() as Site,
  deploys = [older, live] as SiteDeploy[],
} = {}) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config')
      return Promise.resolve({ data: { deploymentMode: 'cloud', sites: true } } as never);
    if (url === '/projects/proj-1/sites/site-1') return Promise.resolve({ data: site } as never);
    if (url === '/projects/proj-1/sites/site-1/deploys')
      return Promise.resolve({ data: deploys } as never);
    if (url === '/projects/proj-1/sites/') return Promise.resolve({ data: [] } as never);
    const role = developerRole(url);
    if (role) return role;
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/sites/site-1']}>
        <Routes>
          <Route path="/project/:projectId/sites/:siteId" element={<SiteDetailPage />} />
          <Route
            path="/project/:projectId/sites"
            element={<div data-testid="sites-list">list</div>}
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('SiteDetailPage', () => {
  beforeEach(() => {
    signInAsMember();
    vi.mocked(api.get).mockReset();
    vi.mocked(api.post).mockReset();
    vi.mocked(api.patch).mockReset();
    vi.mocked(api.delete).mockReset();
  });

  test('shows the live URL', async () => {
    renderPage();
    expect(await screen.findByTestId('site-url')).toHaveAttribute(
      'href',
      'https://shop.example.app',
    );
  });

  test('lists every deploy newest first with time, status, size and who', async () => {
    renderPage({ deploys: [older, live, failed] });
    const rows = await screen.findAllByTestId(/^site-deploy-row-/);
    expect(rows.map((row) => row.dataset.testid)).toEqual([
      'site-deploy-row-dep-3',
      'site-deploy-row-dep-2',
      'site-deploy-row-dep-1',
    ]);
    const liveRow = within(rows[1]);
    expect(liveRow.getByTestId('site-deploy-status-dep-2')).toHaveTextContent('Live');
    expect(rows[1]).toHaveTextContent('2.0 MB');
    expect(rows[1]).toHaveTextContent('12 files');
    expect(rows[1]).toHaveTextContent('user-1');
    expect(rows[1]).toHaveTextContent('abc1234');
    expect(rows[1]).toHaveTextContent(new Date(live.createdAt).toLocaleString());
    expect(within(rows[0]).getByTestId('site-deploy-status-dep-3')).toHaveTextContent('Failed');
    expect(rows[0]).toHaveTextContent('the CDN refused the upload');
    expect(within(rows[2]).getByTestId('site-deploy-status-dep-1')).toHaveTextContent('Succeeded');
  });

  test('offers rollback only to an earlier deploy that succeeded, and asks first', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({
      data: siteFixture({ liveDeployId: 'dep-1' }),
    } as never);
    renderPage({ deploys: [older, live, failed] });
    await screen.findByTestId('site-deploy-row-dep-1');
    expect(screen.queryByTestId('site-rollback-dep-2')).not.toBeInTheDocument();
    expect(screen.queryByTestId('site-rollback-dep-3')).not.toBeInTheDocument();
    await user.click(screen.getByTestId('site-rollback-dep-1'));
    expect(api.post).not.toHaveBeenCalled();
    await user.click(screen.getByTestId('site-rollback-confirm-dep-1'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(
        '/projects/proj-1/sites/site-1/deploys/dep-1/rollback',
        undefined,
        expect.anything(),
      ),
    );
  });

  test('a cancelled rollback changes nothing', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByTestId('site-rollback-dep-1'));
    await user.click(screen.getByTestId('site-rollback-cancel-dep-1'));
    expect(screen.queryByTestId('site-rollback-confirm-dep-1')).not.toBeInTheDocument();
    expect(api.post).not.toHaveBeenCalled();
  });

  test('turns the SPA fallback off for the next deploy', async () => {
    const user = userEvent.setup();
    vi.mocked(api.patch).mockResolvedValue({ data: siteFixture({ spaFallback: false }) } as never);
    renderPage();
    const toggle = await screen.findByTestId('site-spa-toggle');
    expect(toggle).toBeChecked();
    expect(screen.getByTestId('site-spa-note')).toHaveTextContent(/next deploy/i);
    await user.click(toggle);
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith('/projects/proj-1/sites/site-1', {
        spaFallback: false,
      }),
    );
  });

  test('uploads a picked .tar.gz as a deploy', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({
      data: siteDeployFixture({ id: 'dep-9', live: true }),
    } as never);
    renderPage();
    const archive = new File(['gz'], 'dist.tgz', { type: 'application/gzip' });
    await user.upload(await screen.findByTestId('site-upload-file'), archive);
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(
        '/projects/proj-1/sites/site-1/deploy',
        archive,
        expect.anything(),
      ),
    );
    expect(await screen.findByTestId('site-upload-done')).toHaveTextContent(/live/i);
  });

  test('shows why an upload was refused', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue({
      response: { status: 400, data: { error: 'the archive has no index.html at its root' } },
    });
    renderPage();
    await user.upload(
      await screen.findByTestId('site-upload-file'),
      new File(['gz'], 'dist.tgz', { type: 'application/gzip' }),
    );
    expect(await screen.findByTestId('site-upload-error')).toHaveTextContent(
      'the archive has no index.html at its root',
    );
  });

  test('shows the Deploy from CI pipelines for this site, the token kept as a CI secret', async () => {
    const user = userEvent.setup();
    renderPage();
    const snippet = await screen.findByTestId('site-ci-snippet');
    expect(snippet).toHaveTextContent('name: Deploy site to Excalibase');
    expect(snippet).toHaveTextContent('/projects/proj-1/sites/site-1/deploy?label=$GITHUB_SHA');
    expect(snippet).toHaveTextContent('Bearer $EXCALIBASE_TOKEN');
    await user.click(screen.getByTestId('site-ci-tab-curl'));
    expect(screen.getByTestId('site-ci-snippet')).toHaveTextContent('label=$COMMIT_SHA');
    expect(screen.getByTestId('site-ci-secret')).toHaveTextContent('EXCALIBASE_TOKEN');
    await user.click(screen.getByTestId('site-ci-tab-jenkins'));
    expect(screen.getByTestId('site-ci-file')).toHaveTextContent('Jenkinsfile');
  });

  test('deletes the site only once its name is typed', async () => {
    const user = userEvent.setup();
    vi.mocked(api.delete).mockResolvedValue({ data: undefined } as never);
    renderPage();
    await user.click(await screen.findByTestId('site-delete'));
    const confirm = screen.getByTestId('modal-confirm');
    expect(confirm).toBeDisabled();
    await user.type(screen.getByTestId('confirm-input'), 'shop');
    await user.click(confirm);
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith('/projects/proj-1/sites/site-1'));
    expect(await screen.findByTestId('sites-list')).toBeInTheDocument();
  });

  test('a site never deployed explains how to put it live', async () => {
    renderPage({ site: siteFixture({ liveDeployId: undefined }), deploys: [] });
    expect(await screen.findByTestId('site-deploys-empty')).toHaveTextContent(/upload/i);
  });
});
