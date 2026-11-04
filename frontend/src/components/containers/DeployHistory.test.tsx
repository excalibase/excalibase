import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { DeployHistory } from './DeployHistory';
import { ROLLBACK_WINDOW } from './appCopy';
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

const builtImage = `registry.excalibase.internal/proj-1/web@sha256:${'a'.repeat(64)}`;

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

  test('rollback is offered only for the git-built deploys whose images the registry keeps, and the running one', () => {
    const history = Array.from({ length: ROLLBACK_WINDOW + 3 }, (_, index) => {
      const revision = ROLLBACK_WINDOW + 3 - index;
      return deploy({
        id: `dep-${revision}`,
        revision,
        status: 'failed',
        source: 'git-build',
        image: builtImage,
      });
    });
    history[history.length - 1] = deploy({
      id: 'dep-1',
      revision: 1,
      source: 'git-build',
      image: builtImage,
    });
    renderHistory(history);
    expect(screen.getByTestId(`redeploy-dep-${ROLLBACK_WINDOW + 2}`)).toBeInTheDocument();
    expect(screen.getByTestId('redeploy-dep-4')).toBeInTheDocument();
    expect(screen.queryByTestId('redeploy-dep-3')).not.toBeInTheDocument();
    expect(screen.queryByTestId('redeploy-dep-2')).not.toBeInTheDocument();
    expect(screen.getByTestId('redeploy-dep-1')).toBeInTheDocument();
  });

  // Twelve newer deploys put the one asked about past the registry's window, and it is not the running one.
  const pastTheWindow = (oldest: Partial<Deploy>): Deploy[] => {
    const newer = Array.from({ length: ROLLBACK_WINDOW + 2 }, (_, index) => {
      const revision = ROLLBACK_WINDOW + 3 - index;
      return deploy({ id: `dep-${revision}`, revision, source: 'studio' });
    });
    return [...newer, deploy({ id: 'dep-1', revision: 1, status: 'superseded', ...oldest })];
  };

  test('an image deploy past the registry window still rolls back: its registry keeps it', () => {
    renderHistory(
      pastTheWindow({ source: 'api', image: 'ghcr.io/acme/web@sha256:' + 'b'.repeat(64) }),
    );
    expect(screen.getByTestId('redeploy-dep-1')).toBeInTheDocument();
  });

  test('a git-built deploy past the registry window offers no rollback', () => {
    renderHistory(pastTheWindow({ source: 'git-build', image: builtImage }));
    expect(screen.queryByTestId('redeploy-dep-1')).not.toBeInTheDocument();
  });

  test('a rollback of a git-built deploy past the window offers none either: it runs the same swept image', () => {
    const history = pastTheWindow({ source: 'studio', image: builtImage, redeployOf: 'dep-0' });
    renderHistory([
      ...history,
      deploy({
        id: 'dep-0',
        revision: 0,
        status: 'superseded',
        source: 'git-build',
        image: builtImage,
      }),
    ]);
    expect(screen.queryByTestId('redeploy-dep-1')).not.toBeInTheDocument();
  });
});
