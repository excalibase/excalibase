import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { PipelineDeploys } from './PipelineDeploys';
import { ROLLBACK_WINDOW } from './appCopy';
import type { Deploy } from '../../api/apps';

const deploy = (revision: number, status: Deploy['status']): Deploy => ({
  id: `dep-${revision}`,
  appId: 'app-1',
  projectId: 'proj-1',
  revision,
  image: 'registry.excalibase.internal/proj-1/web@sha256:' + 'a'.repeat(64),
  status,
  createdBy: 'dev@example.com',
  createdAt: '2026-10-01T10:00:00Z',
});

describe('PipelineDeploys', () => {
  test('rolls back only to the deploys whose images the registry keeps, and the running one', () => {
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
});
