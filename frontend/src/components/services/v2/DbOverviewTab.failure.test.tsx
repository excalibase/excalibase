import { describe, test, expect, vi, beforeEach } from 'vitest';
import { screen } from '@testing-library/react';
import { DbOverviewTab } from './DbOverviewTab';
import { database, plane, renderTab, resetPlane, settle } from './dbTabTestKit';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } }));

const RAW = 'create db: pod secret-pod-7 CrashLoopBackOff on node n7';

describe('database overview, failed provisioning', () => {
  beforeEach(resetPlane);

  test('says it in plain words and never renders the raw text', async () => {
    const project = database({ currentStage: 'FAILED', failureReason: RAW });
    plane.routes['/provision/p1'] = project;
    const view = renderTab(<DbOverviewTab project={project} />);
    expect(await screen.findByText(/kept crashing/i)).toBeInTheDocument();
    await settle();
    expect(view.container.innerHTML).not.toContain('secret-pod-7');
    expect(view.container.innerHTML).not.toContain('CrashLoopBackOff');
  });

  test('a customer answer with only a kind says it in plain words', async () => {
    const project = database({ currentStage: 'FAILED', failureKind: 'timeout' });
    plane.routes['/provision/p1'] = project;
    renderTab(<DbOverviewTab project={project} />);
    expect(await screen.findByText(/took too long to start/i)).toBeInTheDocument();
  });

  test('the server sentence is shown as it is', async () => {
    const project = database({ currentStage: 'FAILED', failureKind: 'unknown', failureMessage: 'the restore did not complete; restore again from Backups' });
    plane.routes['/provision/p1'] = project;
    renderTab(<DbOverviewTab project={project} />);
    expect(await screen.findByText('the restore did not complete; restore again from Backups')).toBeInTheDocument();
  });

  test('a failed stage with no failure text or kind shows no failure wording', async () => {
    const project = database({ currentStage: 'FAILED' });
    plane.routes['/provision/p1'] = project;
    const view = renderTab(<DbOverviewTab project={project} />);
    await settle();
    expect(view.container.innerHTML).not.toContain('tell us');
  });
});
