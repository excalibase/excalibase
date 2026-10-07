import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import type { ReactElement } from 'react';
import { ProjectLayout } from '../components/layout/ProjectLayout';
import { BackupsPage } from './BackupsPage';
import { SnapshotsPage } from './SnapshotsPage';
import { MigrationsPage } from './MigrationsPage';
import { MetricsPage } from './MetricsPage';
import { PerformancePage } from './PerformancePage';
import { AlertsPage } from './AlertsPage';

// The project in the URL is the only project a project page may act on. The
// project list deliberately starts with another one: the header used to
// pick the first project and every page below followed it.
const URL_PROJECT = 'proj-inurl0001';
const OTHER_PROJECT = 'proj-first0001';

const calls: { method: string; url: string }[] = [];
let historyAnswer: unknown = { metrics: [] };

function answer(url: string): unknown {
  if (url === '/provision') {
    return [
      { projectId: OTHER_PROJECT, tier: 'FREE', status: 'ACTIVE' },
      { projectId: URL_PROJECT, tier: 'FREE', status: 'ACTIVE' },
    ];
  }
  if (url.includes('/metrics/history')) return historyAnswer;
  if (url.endsWith('/backup/list')) return { backups: [], backupEnabled: true, schedule: '', retentionDays: 0 };
  if (/\/(snapshot|migrations|top-queries|wait-events)/.test(url) || url.startsWith('/alerts')) return [];
  return {};
}

vi.mock('../api/client', () => {
  const record = (method: string) => vi.fn(async (url: string) => {
    calls.push({ method, url });
    return { data: answer(url) };
  });
  return { api: { get: record('GET'), post: record('POST'), put: record('PUT'), patch: record('PATCH'), delete: record('DELETE') } };
});

beforeEach(() => {
  calls.length = 0;
  historyAnswer = { metrics: [] };
});

function LocationProbe() {
  const location = useLocation();
  return <div data-testid="location">{location.pathname}</div>;
}

function renderAt(path: string, page: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/project/${URL_PROJECT}/${path}`]}>
        <Routes>
          <Route path="/project/:projectId" element={<ProjectLayout />}>
            <Route path={path} element={page} />
          </Route>
        </Routes>
        <LocationProbe />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

// Every request that names a project names the URL's project.
async function expectOnlyUrlProject(expectedCall: RegExp) {
  await waitFor(() => expect(calls.some((c) => expectedCall.test(`${c.method} ${c.url}`))).toBe(true));
  const scoped = calls.filter((c) => c.url !== '/provision' && c.url !== '/alerts');
  expect(scoped.length).toBeGreaterThan(0);
  for (const call of scoped) {
    expect(call.url, `${call.method} ${call.url}`).not.toContain(OTHER_PROJECT);
  }
}

describe('project pages act on the project in the URL', () => {
  test('Backups lists and triggers a backup of the URL project', async () => {
    renderAt('operations/backups', <BackupsPage />);
    await expectOnlyUrlProject(new RegExp(`^GET /provision/${URL_PROJECT}/backup/list$`));
    fireEvent.click(await screen.findByRole('button', { name: /Trigger Backup/i }));
    await expectOnlyUrlProject(new RegExp(`^POST /provision/${URL_PROJECT}/backup/trigger$`));
  });

  test('Backups restores from the URL project', async () => {
    renderAt('operations/backups', <BackupsPage />);
    fireEvent.click(await screen.findByRole('button', { name: /Restore \/ PITR/i }));
    fireEvent.change(screen.getByLabelText(/New project name/i), { target: { value: 'copy' } });
    fireEvent.click(screen.getByRole('button', { name: /Restore Latest Backup/i }));
    await expectOnlyUrlProject(new RegExp(`^POST /provision/${URL_PROJECT}/backup/restore$`));
  });

  test('Snapshots reads the URL project', async () => {
    renderAt('operations/snapshots', <SnapshotsPage />);
    await expectOnlyUrlProject(new RegExp(`^GET /provision/${URL_PROJECT}/snapshot$`));
  });

  test('Migrations reads the URL project', async () => {
    renderAt('operations/migrations', <MigrationsPage />);
    await expectOnlyUrlProject(new RegExp(`^GET /provision/${URL_PROJECT}/migrations$`));
  });

  test('Metrics reads the URL project', async () => {
    renderAt('monitoring/metrics', <MetricsPage />);
    await expectOnlyUrlProject(new RegExp(`^GET /provision/${URL_PROJECT}/metrics/current$`));
  });

  // A new project has no history yet; the server sends metrics: null.
  test('Metrics renders a project with no history yet', async () => {
    historyAnswer = { metrics: null };
    renderAt('monitoring/metrics', <MetricsPage />);
    await expectOnlyUrlProject(new RegExp(`^GET /provision/${URL_PROJECT}/metrics/history`));
    await new Promise((resolve) => setTimeout(resolve, 200));
    expect(screen.getByTestId('project-switcher')).toHaveTextContent(URL_PROJECT);
  });

  test('Performance reads the URL project', async () => {
    renderAt('monitoring/performance', <PerformancePage />);
    await expectOnlyUrlProject(new RegExp(`^GET /provision/${URL_PROJECT}/performance/summary$`));
  });

  test('Alerts reads the URL project only', async () => {
    renderAt('monitoring/alerts', <AlertsPage />);
    await expectOnlyUrlProject(new RegExp(`^GET /alerts/project/${URL_PROJECT}$`));
    expect(calls.some((c) => c.url === '/alerts')).toBe(false);
  });
});

describe('the header project picker', () => {
  test('shows the URL project', async () => {
    renderAt('operations/backups', <BackupsPage />);
    await waitFor(() => expect(screen.getByTestId('project-switcher')).toHaveTextContent(URL_PROJECT));
  });

  test('navigates to the same page of the picked project instead of re-targeting this one', async () => {
    renderAt('operations/backups', <BackupsPage />);
    fireEvent.click(screen.getByTestId('project-switcher'));
    fireEvent.click(await screen.findByRole('button', { name: new RegExp(OTHER_PROJECT) }));
    expect(screen.getByTestId('location')).toHaveTextContent(`/project/${OTHER_PROJECT}/operations/backups`);
  });
});
