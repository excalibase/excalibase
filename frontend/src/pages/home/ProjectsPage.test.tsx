import { describe, test, expect, vi, beforeEach } from 'vitest';
import { screen, within, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ProjectsPage } from './ProjectsPage';
import { api } from '../../api/client';
import { mountWith, project } from '../../test/homeFixtures';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const rowsOf = () => screen.getAllByRole('row').slice(1);

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
});

describe('Projects page', () => {
  test('lists name and id, what each project contains, its status and an action', async () => {
    mountWith({
      projects: [
        project('p-read', { projectName: 'Reading list', postgresVersion: '16', lastSeenAt: '2026-11-20T00:00:00Z' }),
        project('p-idle', { projectName: 'mcp-test', status: 'PAUSED', pauseReason: 'idle_7d' }),
        project('p-empty', { noDatabase: true }),
        project('foreign', { orgId: 'org-other' }),
      ],
      apps: { 'p-read': [{ id: 'a1', name: 'api', status: 'ACTIVE' }] },
    }, <ProjectsPage />);
    expect(await screen.findByText('1–3 of 3')).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText('Postgres 16 + 1 app')).toBeInTheDocument());
    const [first] = rowsOf();
    expect(within(first).getByRole('link', { name: /^Reading list/ })).toHaveAttribute('href', '/project/p-read');
    expect(within(first).getByText('p-read')).toBeInTheDocument();
    expect(within(first).getByText('Running')).toBeInTheDocument();
    expect(within(first).getByRole('link', { name: 'Open Reading list' })).toHaveAttribute('href', '/project/p-read');
    expect(screen.getByText('Paused · idle')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Resume mcp-test' })).toBeInTheDocument();
    expect(screen.getAllByRole('row').find((r) => r.textContent?.includes('p-empty'))).toHaveTextContent('No services yet');
    expect(screen.getByRole('link', { name: 'Add service p-empty' })).toBeInTheDocument();
    expect(screen.queryByText(/foreign/)).not.toBeInTheDocument();
  });

  test('filters by name, status and what the project contains', async () => {
    mountWith({
      projects: [project('alpha'), project('beta', { status: 'PAUSED' }), project('gamma')],
      sites: { gamma: [{ id: 's1', name: 'blog' }] },
    }, <ProjectsPage />);
    await screen.findByText('1–3 of 3');
    await screen.findByText('Postgres + 1 site');
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Status' }), 'paused');
    expect(rowsOf()).toHaveLength(1);
    expect(within(rowsOf()[0]).getByText('beta')).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Status' }), '');
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Contains' }), 'site');
    expect(rowsOf()).toHaveLength(1);
    expect(within(rowsOf()[0]).getByText('gamma')).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Contains' }), '');
    await userEvent.type(screen.getByRole('searchbox'), 'alp');
    expect(rowsOf()).toHaveLength(1);
  });

  test('sorts by a column and pages ten at a time', async () => {
    const many = Array.from({ length: 12 }, (_, i) => project(`proj-${String(i).padStart(2, '0')}`, { lastSeenAt: `2026-11-${String(i + 1).padStart(2, '0')}T00:00:00Z` }));
    mountWith({ projects: many }, <ProjectsPage />);
    expect(await screen.findByText('1–10 of 12')).toBeInTheDocument();
    expect(within(rowsOf()[0]).getByText('proj-11')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Next page' }));
    expect(screen.getByText('11–12 of 12')).toBeInTheDocument();
    expect(rowsOf()).toHaveLength(2);
    await userEvent.click(screen.getByRole('button', { name: 'Previous page' }));
    await userEvent.click(screen.getByRole('button', { name: 'Project' }));
    expect(within(rowsOf()[0]).getByText('proj-00')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Project' }));
    expect(within(rowsOf()[0]).getByText('proj-11')).toBeInTheDocument();
  });

  test('Resume calls the project resume flow', async () => {
    vi.mocked(api.post).mockResolvedValue({ status: 200, data: { projectId: 'p1', status: 'ACTIVE' } } as never);
    mountWith({ projects: [project('p1', { status: 'PAUSED' })] }, <ProjectsPage />);
    await userEvent.click(await screen.findByRole('button', { name: 'Resume p1' }));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/provision/p1/resume', undefined, expect.anything()));
  });

  test('a failed load is an error, not "no projects"', async () => {
    mountWith({ projects: [], failProjects: true }, <ProjectsPage />);
    expect(await screen.findByRole('alert')).toHaveTextContent(/do not have access/i);
    expect(screen.queryByText(/no projects yet/i)).not.toBeInTheDocument();
  });

  test('no projects shows the empty state and the pause and delete note', async () => {
    mountWith({ projects: [] }, <ProjectsPage />);
    expect(await screen.findByText(/no projects yet/i)).toBeInTheDocument();
    expect(screen.getAllByRole('link', { name: /new project/i })[0]).toHaveAttribute('href', '/provision');
    expect(screen.getByText(/A paused project keeps its data/)).toBeInTheDocument();
  });

  test('uses no cluster wording', async () => {
    mountWith({ projects: [project('p1')] }, <ProjectsPage />);
    await screen.findByText('1–1 of 1');
    expect(document.body.textContent).not.toMatch(/pod|namespace|kubernetes|k8s/i);
  });

  test('with app hosting and sites off a project counts none of them instead of waiting for them', async () => {
    mountWith({ projects: [project('alpha')], config: { appHosting: false, sites: false } }, <ProjectsPage />);
    await screen.findByText('1–1 of 1');
    await waitFor(() => expect(within(rowsOf()[0]).getByText('Postgres')).toBeInTheDocument());
    expect(screen.queryByText('…')).not.toBeInTheDocument();
  });

  test('an apps list that could not be read says so, distinct from none', async () => {
    mountWith({ projects: [project('alpha')], failApps: ['alpha'] }, <ProjectsPage />);
    await screen.findByText('1–1 of 1');
    await waitFor(() => expect(within(rowsOf()[0]).getByText(/apps unavailable/)).toBeInTheDocument());
  });

  test('while loading there is a busy status and no "0 projects" count', async () => {
    mountWith({ projects: [], hangProjects: true }, <ProjectsPage />);
    expect(await screen.findByRole('status')).toHaveTextContent(/loading/i);
    expect(screen.queryByText(/0 projects/)).not.toBeInTheDocument();
  });

  test('after a failed load there is no "0 projects" count either', async () => {
    mountWith({ projects: [], failProjects: true }, <ProjectsPage />);
    expect(await screen.findByRole('alert')).toBeInTheDocument();
    expect(screen.queryByText(/0 projects/)).not.toBeInTheDocument();
  });

  test('shows each project\'s plan and filters by it', async () => {
    mountWith({ projects: [project('alpha'), project('beta', { tier: 'STANDARD' })] }, <ProjectsPage />);
    await screen.findByText('1–2 of 2');
    expect(screen.getByRole('columnheader', { name: /plan/i })).toBeInTheDocument();
    expect(within(rowsOf().find((r) => r.textContent?.includes('beta'))!).getByText('Standard')).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Plan' }), 'STANDARD');
    expect(rowsOf()).toHaveLength(1);
    expect(within(rowsOf()[0]).getByText('beta')).toBeInTheDocument();
  });
});
