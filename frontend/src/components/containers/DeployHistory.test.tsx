import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { DeployHistory } from './DeployHistory';
import type { Deploy } from '../../api/apps';

const deploy = (overrides: Partial<Deploy>): Deploy => ({
  id: 'dep-1',
  appId: 'app-1',
  projectId: 'proj-1',
  revision: 1,
  image: 'nginx:1.26',
  status: 'succeeded',
  createdBy: 'dev@example.com',
  createdAt: '2026-10-01T10:00:00Z',
  ...overrides,
});

function renderHistory(deploys: Deploy[]) {
  render(<DeployHistory deploys={deploys} redeploying={false} onRedeploy={vi.fn()} />);
}

const row = (id: string) => screen.getByTestId(`deploy-row-${id}`);

describe('DeployHistory', () => {
  test('after a redeploy only the newest succeeded revision is Live', () => {
    renderHistory([
      deploy({ id: 'dep-2', revision: 2, image: 'nginx:1.27' }),
      deploy({ id: 'dep-1', revision: 1 }),
    ]);
    expect(row('dep-2')).toHaveTextContent('Live');
    expect(row('dep-1')).not.toHaveTextContent('Live');
    expect(row('dep-1')).toHaveTextContent('Replaced by a newer deploy');
  });

  test('a rollback makes the revision it rolled out the only Live one', () => {
    renderHistory([
      deploy({ id: 'dep-3', revision: 3, redeployOf: 'dep-1' }),
      deploy({ id: 'dep-2', revision: 2, image: 'nginx:1.27' }),
      deploy({ id: 'dep-1', revision: 1 }),
    ]);
    expect(screen.getAllByText('Live')).toHaveLength(1);
    expect(row('dep-3')).toHaveTextContent('Live');
  });

  test('the previous revision stays Live while a newer one rolls out or after it failed', () => {
    renderHistory([
      deploy({ id: 'dep-3', revision: 3, status: 'rolling' }),
      deploy({ id: 'dep-2', revision: 2, status: 'failed' }),
      deploy({ id: 'dep-1', revision: 1 }),
    ]);
    expect(row('dep-3')).toHaveTextContent('Rolling out');
    expect(row('dep-2')).toHaveTextContent('Failed');
    expect(row('dep-1')).toHaveTextContent('Live');
  });
});
