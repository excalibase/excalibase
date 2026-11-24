import { describe, test, expect, vi, beforeEach } from 'vitest';
import { screen, within } from '@testing-library/react';
import { UsagePage } from './UsagePage';
import { mountWith, project } from '../test/homeFixtures';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const FREE = { tier: 'FREE', version: 0, maxProjects: 2, instances: 1, storageSize: '5Gi', maxStorageSize: '5Gi', maxApps: 2, cpu: '0.5', memory: '512Mi' };

beforeEach(() => vi.clearAllMocks());

describe('Usage page', () => {
  test('shows the org plan and its use against the plan limits', async () => {
    mountWith({
      projects: [project('p1', { projectName: 'Reading list', storageSize: '5Gi' }), project('p2', { noDatabase: true })],
      tiers: [FREE],
      apps: { p1: [{ id: 'a1' }] },
    }, <UsagePage />);
    expect(await screen.findByRole('heading', { name: 'Usage & credits' })).toBeInTheDocument();
    const plan = within(await screen.findByRole('region', { name: 'Plan' }));
    expect(plan.getByText('Free')).toBeInTheDocument();
    expect(plan.getByText('0.5 CPU, 512Mi memory per database')).toBeInTheDocument();
    const use = within(screen.getByRole('region', { name: 'Usage' }));
    expect(await use.findByText('Projects')).toBeInTheDocument();
    expect(use.getByText('Projects').nextSibling).toHaveTextContent('2 of 2');
    expect(use.getByText('Databases').nextSibling).toHaveTextContent('1');
    expect(use.getByText('Disk allocated').nextSibling).toHaveTextContent('5 GiB');
    const row = within(await screen.findByRole('row', { name: /Reading list/ }));
    expect(row.getByText('1 of 2')).toBeInTheDocument();
  });

  test('has no stub text and invents no money figure', async () => {
    mountWith({ projects: [project('p1')], tiers: [FREE] }, <UsagePage />);
    await screen.findByRole('region', { name: 'Plan' });
    expect(screen.queryByText(/soon/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/\$/)).not.toBeInTheDocument();
  });

  test('a plan with no project cap says so instead of showing 0', async () => {
    mountWith({ projects: [project('p1')], tiers: [{ ...FREE, maxProjects: 0 }] }, <UsagePage />);
    const use = within(await screen.findByRole('region', { name: 'Usage' }));
    expect(await use.findByText('Projects')).toBeInTheDocument();
    expect(use.getByText('Projects').nextSibling).toHaveTextContent('1 (no limit)');
  });

  test('limits the plan list cannot give stay out; counts remain', async () => {
    mountWith({ projects: [project('p1')] }, <UsagePage />);
    const use = within(await screen.findByRole('region', { name: 'Usage' }));
    expect(await use.findByText('Projects')).toBeInTheDocument();
    expect(use.getByText('Projects').nextSibling).toHaveTextContent(/^1$/);
  });

  test('a failed project list shows an error, not zeros', async () => {
    mountWith({ projects: [], failProjects: true, tiers: [FREE] }, <UsagePage />);
    expect(await screen.findByRole('alert')).toHaveTextContent(/could not load/i);
    expect(screen.queryByRole('region', { name: 'Usage' })).not.toBeInTheDocument();
  });

  test('a self-hosted install reads "no limit" for projects, whatever the plan list says', async () => {
    mountWith({ projects: [project('p1')], tiers: [FREE], config: { deploymentMode: 'selfhosted' } }, <UsagePage />);
    const use = within(await screen.findByRole('region', { name: 'Usage' }));
    expect(use.getByText('Projects').nextSibling).toHaveTextContent('1 (no limit)');
  });

  test('with app hosting off the Apps column shows a dash, not 0 of 2', async () => {
    mountWith({ projects: [project('p1', { projectName: 'Solo' })], tiers: [FREE], config: { appHosting: false } }, <UsagePage />);
    const row = within(await screen.findByRole('row', { name: /Solo/ }));
    expect(row.queryByText(/of 2/)).not.toBeInTheDocument();
  });

  test('the disk total is marked partial when a project reports no size', async () => {
    mountWith({ projects: [project('p1', { storageSize: '5Gi' }), project('p2')], tiers: [FREE] }, <UsagePage />);
    expect(await screen.findByText('≥ 5 GiB (some unknown)')).toBeInTheDocument();
  });
});
