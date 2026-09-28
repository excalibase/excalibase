import { describe, test, expect } from 'vitest';
import { databaseServiceStatus, containersSummary } from './serviceStatus';
import type { DatabaseInstance } from '../../types';

const project = (status: string, currentStage = 'COMPLETED') =>
  ({ status, currentStage }) as unknown as Pick<DatabaseInstance, 'status' | 'currentStage'>;

describe('databaseServiceStatus', () => {
  test.each([
    ['ACTIVE', 'COMPLETED', 'Running', 'success'],
    ['PROVISIONING', 'CRD_DEPLOYMENT', 'Provisioning', 'progress'],
    ['FAILED', 'FAILED', 'Failed', 'error'],
    ['PAUSED', 'COMPLETED', 'Paused', 'warning'],
    ['PAUSING', 'COMPLETED', 'Pausing', 'progress'],
    ['RESUMING', 'COMPLETED', 'Resuming', 'progress'],
    ['RESTORING', 'COMPLETED', 'Restoring', 'progress'],
    ['PENDING_DELETION', 'COMPLETED', 'Scheduled for deletion', 'warning'],
    ['DELETING', 'COMPLETED', 'Deleting', 'progress'],
  ])('%s reads as %s', (status, stage, label, tone) => {
    expect(databaseServiceStatus(project(status, stage))).toEqual({ label, tone });
  });

  test('a failed pipeline reads as failed whatever the status says', () => {
    expect(databaseServiceStatus(project('PROVISIONING', 'FAILED'))).toEqual({
      label: 'Failed',
      tone: 'error',
    });
  });

  test('an unknown status is shown as it is, not guessed', () => {
    expect(databaseServiceStatus(project('SOMETHING_NEW'))).toEqual({
      label: 'SOMETHING_NEW',
      tone: 'neutral',
    });
  });
});

describe('containersSummary', () => {
  test('says nothing is running when there are no containers', () => {
    expect(containersSummary([])).toBe('No containers yet');
  });

  test('counts containers in the singular and plural', () => {
    expect(containersSummary([{ id: 'a' }])).toBe('1 container');
    expect(containersSummary([{ id: 'a' }, { id: 'b' }])).toBe('2 containers');
  });
});
