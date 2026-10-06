import { describe, test, expect, beforeEach, vi } from 'vitest';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ClusterSettingsCard } from './ClusterSettingsCard';
import { api } from '../api/client';
import type { DatabaseInstance } from '../types';
import type { ClusterSettings } from '../api/clusterSettings';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn() } }));

const settings: ClusterSettings = {
  projectId: 'p-1',
  tier: 'FREE',
  orgTier: 'FREE',
  storageSize: '2Gi',
  storageStart: '5Gi',
  storageLimit: '5Gi',
  storageUsedBytes: 1073741824,
  canChange: true,
  instances: 1,
  cpu: '0.5',
  memory: '512Mi',
  parameters: { work_mem: '4MB' },
  tunableParameters: ['jit', 'work_mem'],
};

function project(overrides: Partial<DatabaseInstance> = {}): DatabaseInstance {
  return { projectId: 'p-1', status: 'ACTIVE', ...overrides } as DatabaseInstance;
}

function renderCard(instance: DatabaseInstance, current = settings) {
  vi.mocked(api.get).mockResolvedValue({ data: current } as never);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <ClusterSettingsCard project={instance} />
    </QueryClientProvider>,
  );
}

describe('ClusterSettingsCard', () => {
  beforeEach(() => vi.clearAllMocks());

  test('shows the disk against the plan and the size the cluster runs', async () => {
    renderCard(project());
    const card = await screen.findByTestId('cluster-settings-card');
    await waitFor(() => expect(card).toHaveTextContent('1.00 GB used'));
    expect(card).toHaveTextContent('disk 2Gi');
    expect(card).toHaveTextContent('grows up to 5Gi');
    expect(card).toHaveTextContent('1 instance');
    expect(card).toHaveTextContent('512Mi');
  });

  test('grows the disk only after confirmation, then shows the answered size', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockResolvedValueOnce({
      data: { ...settings, storageSize: '4Gi' },
    } as never);
    renderCard(project());

    await user.type(await screen.findByTestId('resize-input'), '4');
    await user.click(screen.getByTestId('resize-btn'));
    expect(api.post).not.toHaveBeenCalled();
    await user.click(await screen.findByTestId('modal-confirm'));

    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/provision/p-1/storage', { size: '4Gi' }),
    );
    await waitFor(() =>
      expect(screen.getByTestId('cluster-settings-card')).toHaveTextContent('disk 4Gi'),
    );
  });

  test('never offers a size at or below the current disk', async () => {
    const user = userEvent.setup();
    renderCard(project());
    await user.type(await screen.findByTestId('resize-input'), '2');
    expect(screen.getByTestId('resize-btn')).toBeDisabled();
  });

  test('shows the control plane reason when a change is refused', async () => {
    const user = userEvent.setup();
    vi.mocked(api.put).mockRejectedValueOnce({
      response: { data: { error: 'postgres parameter not allowed: work_mem' } },
    });
    renderCard(project());

    const row = await screen.findByTestId('param-work_mem');
    await user.clear(within(row).getByRole('textbox'));
    await user.type(within(row).getByRole('textbox'), '1GB');
    await user.click(screen.getByTestId('params-save-btn'));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/provision/p-1/parameters', {
        parameters: { work_mem: '1GB' },
      }),
    );
    expect(await screen.findByTestId('cluster-settings-error')).toHaveTextContent(
      'postgres parameter not allowed',
    );
  });

  // EXC-555: the second of two saves answered "project is busy", hiding the
  // first one's real reason.
  test('a double-clicked save sends one request', async () => {
    let finish: (value: unknown) => void = () => {};
    vi.mocked(api.put).mockImplementation(() => new Promise((resolve) => { finish = resolve; }) as never);
    renderCard(project());
    const saveButton = await screen.findByTestId('params-save-btn');
    fireEvent.click(saveButton);
    fireEvent.click(saveButton);
    finish({ data: settings });
    await waitFor(() => expect(api.put).toHaveBeenCalledTimes(1));
  });

  test('sends only the settings that have a value', async () => {
    const user = userEvent.setup();
    vi.mocked(api.put).mockResolvedValueOnce({
      data: { ...settings, parameters: { jit: 'off' } },
    } as never);
    renderCard(project());

    await user.clear(within(await screen.findByTestId('param-work_mem')).getByRole('textbox'));
    await user.type(within(screen.getByTestId('param-jit')).getByRole('textbox'), 'off');
    await user.click(screen.getByTestId('params-save-btn'));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/provision/p-1/parameters', {
        parameters: { jit: 'off' },
      }),
    );
  });

  test('each setting shows an example value and the units it takes', async () => {
    renderCard(project());
    expect(
      within(await screen.findByTestId('param-work_mem')).getByRole('textbox'),
    ).toHaveAttribute('placeholder', '4MB');
    expect(within(screen.getByTestId('param-jit')).getByRole('textbox')).toHaveAttribute(
      'placeholder',
      'off',
    );
    expect(screen.getByText(/Memory takes kB, MB, GB or TB/)).toBeInTheDocument();
  });

  // The tier follows the organization; the card can only apply the org's plan.
  test('offers to move onto the organization plan when it differs', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockResolvedValueOnce({
      data: { ...settings, tier: 'STANDARD', orgTier: 'STANDARD' },
    } as never);
    renderCard(project(), { ...settings, orgTier: 'STANDARD' });

    await user.click(await screen.findByTestId('tier-apply-btn'));
    await user.click(await screen.findByTestId('modal-confirm'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/provision/p-1/tier', { tier: 'STANDARD' }),
    );
  });

  test('has nothing to apply when the project is on the organization plan', async () => {
    renderCard(project());
    expect(await screen.findByTestId('tier-apply-btn')).toBeDisabled();
  });

  test('is closed on a project that is not active', async () => {
    renderCard(project({ status: 'PAUSED' }));
    expect(await screen.findByTestId('cluster-settings-inactive')).toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalled();
  });
});

describe('ClusterSettingsCard states', () => {
  beforeEach(() => vi.clearAllMocks());

  test('shows why the settings could not be read', async () => {
    vi.mocked(api.get).mockRejectedValueOnce(new Error('network down'));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <ClusterSettingsCard project={project()} />
      </QueryClientProvider>,
    );
    expect(await screen.findByText('network down')).toBeInTheDocument();
  });

  test('never offers a disk above the plan', async () => {
    const user = userEvent.setup();
    renderCard(project());
    await user.type(await screen.findByTestId('resize-input'), '6');
    expect(screen.getByTestId('resize-btn')).toBeDisabled();
  });

  test('shows the reason a plan change was refused', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockRejectedValueOnce({
      response: { data: { error: 'the platform has no room for the plan' } },
    });
    renderCard(project(), { ...settings, orgTier: 'STANDARD' });
    await user.click(await screen.findByTestId('tier-apply-btn'));
    await user.click(await screen.findByTestId('modal-confirm'));
    expect(await screen.findByTestId('cluster-settings-error')).toHaveTextContent('no room');
  });
});

describe('ClusterSettingsCard limits and roles', () => {
  beforeEach(() => vi.clearAllMocks());

  // Enterprise grows to 2Ti: the ceiling is read in TiB as well as GiB.
  test('offers growth up to a maximum given in TiB', async () => {
    const user = userEvent.setup();
    renderCard(project(), {
      ...settings,
      storageSize: '500Gi',
      storageStart: '500Gi',
      storageLimit: '2Ti',
    });
    await user.type(await screen.findByTestId('resize-input'), '2048');
    expect(screen.getByTestId('resize-btn')).toBeEnabled();
    await user.clear(screen.getByTestId('resize-input'));
    await user.type(screen.getByTestId('resize-input'), '2049');
    expect(screen.getByTestId('resize-btn')).toBeDisabled();
  });

  test('says a disk that cannot grow is fixed', async () => {
    renderCard(project(), { ...settings, storageSize: '5Gi' });
    await waitFor(() =>
      expect(screen.getByTestId('cluster-settings-card')).toHaveTextContent('fixed'),
    );
  });

  test('says when the space used could not be read', async () => {
    renderCard(project(), { ...settings, storageUsedBytes: null });
    await waitFor(() =>
      expect(screen.getByTestId('cluster-settings-card')).toHaveTextContent('used: unknown'),
    );
  });

  // Changing size, plan or settings is for admins; others only read.
  test('hides every change from a caller who may not make it', async () => {
    renderCard(project(), { ...settings, canChange: false, orgTier: 'STANDARD' });
    await waitFor(() =>
      expect(screen.getByTestId('cluster-settings-card')).toHaveTextContent('disk 2Gi'),
    );
    expect(screen.queryByTestId('resize-btn')).not.toBeInTheDocument();
    expect(screen.queryByTestId('tier-apply-btn')).not.toBeInTheDocument();
    expect(screen.queryByTestId('params-save-btn')).not.toBeInTheDocument();
    expect(screen.getByTestId('param-work_mem')).toHaveTextContent('4MB');
  });
});
