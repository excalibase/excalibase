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
});
