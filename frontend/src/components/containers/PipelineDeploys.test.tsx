import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { PipelineDeploys } from './PipelineDeploys';
import { ROLLBACK_WINDOW } from './appCopy';
import type { Deploy } from '../../api/apps';

const builtImage = 'registry.excalibase.internal/proj-1/web@sha256:' + 'a'.repeat(64);

const deploy = (
  revision: number,
  status: Deploy['status'],
  overrides: Partial<Deploy> = {},
): Deploy => ({
  id: `dep-${revision}`,
  appId: 'app-1',
  projectId: 'proj-1',
  revision,
  image: builtImage,
  source: 'git-build',
  status,
  createdBy: 'dev@example.com',
  createdAt: '2026-10-01T10:00:00Z',
  ...overrides,
});

describe('PipelineDeploys', () => {
  test('rolls back only to the git-built deploys whose images the registry keeps, and the running one', () => {
    const total = ROLLBACK_WINDOW + 3;
    const deploys = Array.from({ length: total }, (_, index) => deploy(total - index, 'failed'));
    deploys[total - 1] = deploy(1, 'succeeded');
    render(<PipelineDeploys deploys={deploys} busy={false} onRedeploy={vi.fn()} />);
    expect(screen.getByTestId(`redeploy-dep-${total}`)).toBeInTheDocument();
    expect(screen.getByTestId('rollback-dep-4')).toBeInTheDocument();
    expect(screen.queryByTestId('rollback-dep-3')).not.toBeInTheDocument();
    expect(screen.queryByTestId('rollback-dep-2')).not.toBeInTheDocument();
    expect(screen.getByTestId('rollback-dep-1')).toBeInTheDocument();
  });

  test('an image deploy at index 12 still rolls back; a git-built one there does not', () => {
    const total = ROLLBACK_WINDOW + 3;
    const newer = Array.from({ length: total - 1 }, (_, index) =>
      deploy(total - index, 'succeeded', { source: 'studio', image: 'nginx:1.27' }),
    );
    const image = deploy(1, 'superseded', {
      source: 'api',
      image: 'ghcr.io/acme/web@sha256:' + 'b'.repeat(64),
    });
    const { unmount } = render(
      <PipelineDeploys deploys={[...newer, image]} busy={false} onRedeploy={vi.fn()} />,
    );
    expect(screen.getByTestId('rollback-dep-1')).toBeInTheDocument();
    unmount();
    render(
      <PipelineDeploys
        deploys={[...newer, deploy(1, 'superseded')]}
        busy={false}
        onRedeploy={vi.fn()}
      />,
    );
    expect(screen.queryByTestId('rollback-dep-1')).not.toBeInTheDocument();
  });
});
