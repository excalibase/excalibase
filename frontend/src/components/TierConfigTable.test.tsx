import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { TierConfigTable } from './TierConfigTable';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), put: vi.fn() } }));

const standard = {
  tier: 'STANDARD',
  version: 4,
  maxProjects: 5,
  instances: 3,
  storageSize: '50Gi',
  maxStorageSize: '500Gi',
  maxAppDiskSize: '20Gi',
  maxApps: 5,
  memory: '4Gi',
  cpu: '2',
  backupEnabled: true,
  autoPauseAfterDays: 0,
  maxSites: 20,
  maxSiteDeployBytes: 104857600,
  maxSiteFiles: 20000,
  maxSiteFileBytes: 26214400,
  maxSiteDeploysPerHour: 120,
  maxSiteCustomDomains: 5,
  siteBuildCpuMillis: 2000,
  siteBuildMemory: '4Gi',
  siteBuildDisk: '10Gi',
  siteBuildTimeoutSeconds: 1200,
  siteBuildConcurrency: 2,
};

function renderTable({ sites = true, siteGitBuilds = false } = {}) {
  vi.mocked(api.get).mockImplementation((url: string) =>
    Promise.resolve({
      data: url === '/config' ? { deploymentMode: 'cloud', sites, siteGitBuilds } : [standard],
    } as never),
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <TierConfigTable canMutate />
    </QueryClientProvider>,
  );
}

// Studio sends back the version it read, so a save never undoes another admin's (EXC-620).
const ifMatchVersion4 = { headers: { 'If-Match': '4' } };

describe('TierConfigTable', () => {
  beforeEach(() => vi.clearAllMocks());

  // A plan's disk has a starting size and a maximum a project may grow to (EXC-492).
  test('edits and saves the maximum disk with the rest of the plan', async () => {
    const user = userEvent.setup();
    vi.mocked(api.put).mockResolvedValueOnce({ data: standard } as never);
    renderTable();

    const max = await screen.findByDisplayValue('500Gi');
    await user.clear(max);
    await user.type(max, '1Ti');
    await user.click(screen.getByRole('button', { name: /save/i }));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        '/admin/tiers/STANDARD',
        expect.objectContaining({ storageSize: '50Gi', maxStorageSize: '1Ti' }), ifMatchVersion4,),
    );
  });

  // The largest disk one container may have on the plan (EXC-523).
  test('edits and saves the app disk cap with the rest of the plan', async () => {
    const user = userEvent.setup();
    vi.mocked(api.put).mockResolvedValueOnce({ data: standard } as never);
    renderTable();

    const cap = await screen.findByDisplayValue('20Gi');
    await user.clear(cap);
    await user.type(cap, '40Gi');
    await user.click(screen.getByRole('button', { name: /save/i }));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        '/admin/tiers/STANDARD',
        expect.objectContaining({ maxStorageSize: '500Gi', maxAppDiskSize: '40Gi' }), ifMatchVersion4,),
    );
  });

  // How many containers one project may hold on the plan (EXC-524).
  test('edits and saves the app count with the rest of the plan', async () => {
    const user = userEvent.setup();
    vi.mocked(api.put).mockResolvedValueOnce({ data: standard } as never);
    renderTable();

    const count = await screen.findByLabelText('Max apps for STANDARD');
    await user.clear(count);
    await user.type(count, '8');
    await user.click(screen.getByRole('button', { name: /save/i }));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/admin/tiers/STANDARD', expect.objectContaining({ maxApps: 8, maxAppDiskSize: '20Gi' }), ifMatchVersion4),
    );
  });

  // A plan's site count and deploy rate (EXC-620), saved with the rest of the plan.
  test('edits and saves the site count and deploy rate with the rest of the plan', async () => {
    const user = userEvent.setup();
    vi.mocked(api.put).mockResolvedValueOnce({ data: standard } as never);
    renderTable();

    const sitesCount = await screen.findByLabelText('Max sites for STANDARD');
    await user.clear(sitesCount);
    await user.type(sitesCount, '25');
    const rate = screen.getByLabelText('Site deploys an hour for STANDARD');
    await user.clear(rate);
    await user.type(rate, '60');
    await user.click(screen.getByRole('button', { name: /save/i }));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        '/admin/tiers/STANDARD',
        expect.objectContaining({
          maxSites: 25,
          maxSiteDeploysPerHour: 60,
          maxSiteDeployBytes: 104857600,
          maxSiteFiles: 20000,
          maxSiteFileBytes: 26214400,
          maxApps: 5,
        }), ifMatchVersion4,),
    );
  });

  // Custom domains per project follow the plan too (EXC-614).
  test('edits and saves the custom domains a project may hold', async () => {
    const user = userEvent.setup();
    vi.mocked(api.put).mockResolvedValueOnce({ data: standard } as never);
    renderTable();

    const domains = await screen.findByLabelText('Custom domains per project for STANDARD');
    expect(domains).toHaveValue(5);
    await user.clear(domains);
    await user.type(domains, '8');
    await user.click(screen.getByRole('button', { name: /save/i }));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        '/admin/tiers/STANDARD',
        expect.objectContaining({ maxSiteCustomDomains: 8, maxSites: 20 }),
        ifMatchVersion4,
      ),
    );
  });

  // A git build's limits come from the plan (EXC-354); the time is edited in minutes.
  test('edits a git build memory and minutes with the plan', async () => {
    const user = userEvent.setup();
    vi.mocked(api.put).mockResolvedValueOnce({ data: standard } as never);
    renderTable({ siteGitBuilds: true });

    const minutes = await screen.findByLabelText('Build minutes for STANDARD');
    expect(minutes).toHaveValue(20);
    await user.clear(minutes);
    await user.type(minutes, '30');
    await user.click(screen.getByRole('button', { name: /save/i }));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        '/admin/tiers/STANDARD',
        expect.objectContaining({ siteBuildTimeoutSeconds: 1800, siteBuildMemory: '4Gi', siteBuildCpuMillis: 2000 }),
        ifMatchVersion4,
      ),
    );
  });

  test('shows no build limits while git builds are off', async () => {
    renderTable();
    expect(await screen.findByLabelText('Max sites for STANDARD')).toBeInTheDocument();
    expect(screen.queryByLabelText('Build minutes for STANDARD')).not.toBeInTheDocument();
  });

  // Sites stays behind its install flag: no Sites, no site columns.
  test('shows no site limits on an install without Sites', async () => {
    renderTable({ sites: false });
    expect(await screen.findByLabelText('Max apps for STANDARD')).toBeInTheDocument();
    expect(screen.queryByLabelText('Max sites for STANDARD')).not.toBeInTheDocument();
  });

  // Small plans may cap app disks in mebibytes (EXC-523).
  test('saves an app disk cap in Mi and explains the accepted units', async () => {
    const user = userEvent.setup();
    renderTable();

    const cap = await screen.findByDisplayValue('20Gi');
    await user.clear(cap);
    await user.type(cap, '500Mi');
    await user.click(screen.getByRole('button', { name: /save/i }));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        '/admin/tiers/STANDARD',
        expect.objectContaining({ maxAppDiskSize: '500Mi' }), ifMatchVersion4,),
    );
    expect(screen.getByRole('columnheader', { name: /max app disk/i })).toHaveAttribute(
      'title',
      expect.stringMatching(/Mi.*Gi.*0Gi = none/),
    );
  });

  test('an app disk cap that is not whole Mi or Gi is not saved', async () => {
    const user = userEvent.setup();
    renderTable();

    const cap = await screen.findByDisplayValue('20Gi');
    await user.clear(cap);
    await user.type(cap, '1.5GB');

    expect(screen.getByRole('button', { name: /save/i })).toBeDisabled();
    expect(screen.getByTestId('app-disk-cap-error-STANDARD')).toHaveTextContent(/Mi or.*Gi/);
  });
  test("a refused save alerts the server's reason, not the transport text", async () => {
    const user = userEvent.setup();
    const alert = vi.spyOn(globalThis, 'alert').mockImplementation(() => {});
    vi.mocked(api.put).mockRejectedValueOnce({
      message: 'Request failed with status code 409',
      response: { status: 409, data: { error: 'instances must be odd' } },
    });
    renderTable();

    // Every field of the row is edited, so each one reaches the request.
    await user.clear(await screen.findByDisplayValue('2'));
    await user.type(screen.getByPlaceholderText('0.5'), '4');
    await user.clear(screen.getByDisplayValue('4Gi'));
    await user.type(screen.getByPlaceholderText('4Gi'), '8Gi');
    await user.clear(screen.getByDisplayValue('50Gi'));
    await user.type(screen.getByPlaceholderText('50Gi'), '60Gi');
    const [, instances, maxProjects, autoPause] = screen.getAllByRole('spinbutton');
    await user.clear(instances);
    await user.type(instances, '4');
    await user.clear(maxProjects);
    await user.type(maxProjects, '9');
    await user.clear(autoPause);
    await user.type(autoPause, '7');
    await user.click(screen.getByRole('checkbox'));
    await user.click(screen.getByRole('button', { name: /save/i }));

    await waitFor(() => expect(alert).toHaveBeenCalledWith('Save failed: instances must be odd'));
    expect(api.put).toHaveBeenCalledWith(
      '/admin/tiers/STANDARD',
      expect.objectContaining({
        cpu: '4', memory: '8Gi', storageSize: '60Gi', instances: 4, maxProjects: 9, autoPauseAfterDays: 7, backupEnabled: false,
      }), ifMatchVersion4,);
    alert.mockRestore();
  });

  test('a read-only viewer cannot edit or save', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [standard] } as never);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <TierConfigTable canMutate={false} />
      </QueryClientProvider>,
    );
    expect(await screen.findByDisplayValue('500Gi')).toBeDisabled();
    expect(screen.getByText(/Read-only/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /save/i })).toBeDisabled();
  });
  // Save turns on for a change to any one field, a plan without an auto-pause setting included.
  test.each([
    ['instances', 1],
    ['max projects', 2],
    ['auto-pause', 3],
    ['backup', -1],
  ])('editing only %s enables save', async (_field, spinIndex) => {
    const user = userEvent.setup();
    vi.mocked(api.get).mockResolvedValue({ data: [{ ...standard, autoPauseAfterDays: undefined }] } as never);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <TierConfigTable canMutate />
      </QueryClientProvider>,
    );
    await screen.findByDisplayValue('500Gi');
    if (spinIndex < 0) {
      await user.click(screen.getByRole('checkbox'));
    } else {
      await user.type(screen.getAllByRole('spinbutton')[spinIndex], '1');
    }
    expect(screen.getByRole('button', { name: /save/i })).toBeEnabled();
  });

  test('a save made from a version someone else replaced is refused and the plan reloaded', async () => {
    const user = userEvent.setup();
    const alert = vi.spyOn(globalThis, 'alert').mockImplementation(() => {});
    vi.mocked(api.put).mockRejectedValueOnce({
      message: 'Request failed with status code 412',
      response: { status: 412, data: { error: 'the tier was changed by someone else: it is now at version 5' } },
    });
    renderTable();
    const max = await screen.findByDisplayValue('500Gi');
    await user.clear(max);
    await user.type(max, '1Ti');
    const listCalls = vi.mocked(api.get).mock.calls.filter(([url]) => url === '/tiers').length;
    await user.click(screen.getByRole('button', { name: /save/i }));

    await waitFor(() => expect(alert).toHaveBeenCalledWith(expect.stringMatching(/changed by someone else.*reloaded/i)));
    await waitFor(() =>
      expect(vi.mocked(api.get).mock.calls.filter(([url]) => url === '/tiers').length).toBeGreaterThan(listCalls),
    );
    alert.mockRestore();
  });
});
