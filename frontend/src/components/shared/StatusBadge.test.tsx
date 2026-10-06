import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { StatusBadge } from './StatusBadge';

function spins(container: HTMLElement) {
  return container.querySelector('.animate-spin') !== null;
}

describe('StatusBadge', () => {
  it.each([
    ['ACTIVE', 'Active'],
    ['FAILED', 'Failed'],
    ['PAUSED', 'Paused'],
    ['DEPROVISIONED', 'Deprovisioned'],
    ['HEALTHY', 'Healthy'],
    ['DEGRADED', 'Degraded'],
    ['DOWN', 'Down'],
    ['BACKUPS_PENDING_DELETE', 'Deleted, backups pending purge'],
  ])('settled status %s reads %s without a spinner', (status, label) => {
    const { container } = render(<StatusBadge status={status} />);
    expect(screen.getByText(label, { exact: false })).toBeInTheDocument();
    expect(spins(container)).toBe(false);
  });

  it.each([
    ['PROVISIONING', 'Provisioning'],
    ['PAUSING', 'Pausing'],
    ['RESUMING', 'Resuming'],
    ['DELETING', 'Deleting'],
    ['RESTORING', 'Restoring'],
    ['IN_PROGRESS', 'In progress'],
    ['RUNNING', 'In progress'],
  ])('in-progress status %s reads %s with a spinner', (status, label) => {
    const { container } = render(<StatusBadge status={status} />);
    expect(screen.getByText(label, { exact: false })).toBeInTheDocument();
    expect(spins(container)).toBe(true);
  });

  it.each(['VALIDATING', 'NAMESPACE_CREATION', 'CRD_DEPLOYMENT', 'WAITING_FOR_READY', 'CREDENTIAL_GENERATION',
    'BACKUP_CONFIGURATION', 'METRICS_SETUP', 'WATCHER_DEPLOYMENT', 'CONTAINER_CREATION', 'ROLE_CREATION'])(
    'pipeline stage %s spins as Provisioning', (stage) => {
      const { container } = render(<StatusBadge stage={stage as never} />);
      expect(screen.getByText('Provisioning', { exact: false })).toBeInTheDocument();
      expect(spins(container)).toBe(true);
    });

  it('a completed pipeline stage reads Active', () => {
    render(<StatusBadge stage="COMPLETED" />);
    expect(screen.getByText('Active', { exact: false })).toBeInTheDocument();
  });

  it('a completed backup reads Completed', () => {
    render(<StatusBadge status="COMPLETED" />);
    expect(screen.getByText('Completed', { exact: false })).toBeInTheDocument();
  });

  it('PENDING_DELETION reads Scheduled for deletion with the purge date, never Provisioning', () => {
    const { container } = render(
      <StatusBadge status="PENDING_DELETION" stage="COMPLETED" deletionDueAt="2026-10-12T10:00:00Z" />,
    );
    const badge = screen.getByText('Scheduled for deletion', { exact: false });
    expect(badge.textContent).toMatch(/2026/);
    expect(screen.queryByText(/Provisioning/)).toBeNull();
    expect(spins(container)).toBe(false);
  });

  it('PENDING_DELETION without a date still reads Scheduled for deletion', () => {
    render(<StatusBadge status="PENDING_DELETION" />);
    expect(screen.getByText('Scheduled for deletion', { exact: false })).toBeInTheDocument();
  });

  it('the project status wins over a stale pipeline stage', () => {
    render(<StatusBadge status="PAUSED" stage="COMPLETED" />);
    expect(screen.getByText('Paused', { exact: false })).toBeInTheDocument();
  });

  it('while provisioning, the pipeline stage still drives the badge', () => {
    render(<StatusBadge status="PROVISIONING" stage="FAILED" />);
    expect(screen.getByText('Failed', { exact: false })).toBeInTheDocument();
  });

  it('an unknown value reads Unknown with the raw value and does not spin', () => {
    const { container } = render(<StatusBadge status="SOMETHING_NEW" />);
    expect(screen.getByText(/Unknown/)).toBeInTheDocument();
    expect(screen.getByText(/SOMETHING_NEW/)).toBeInTheDocument();
    expect(screen.queryByText(/Provisioning/)).toBeNull();
    expect(spins(container)).toBe(false);
  });

  it('no value at all reads Unknown, not Provisioning', () => {
    const { container } = render(<StatusBadge />);
    expect(screen.getByText(/Unknown/)).toBeInTheDocument();
    expect(spins(container)).toBe(false);
  });
});
