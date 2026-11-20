import { describe, test, expect } from 'vitest';
import { describeBackupSchedule } from './backupSchedule';

describe('describeBackupSchedule', () => {
  test('daily', () => {
    expect(describeBackupSchedule('0 2 * * *')).toBe('Nightly backups at 02:00 UTC');
    expect(describeBackupSchedule('30 14 * * *')).toBe('Daily backups at 14:30 UTC');
  });

  test('hourly', () => {
    expect(describeBackupSchedule('0 * * * *')).toBe('Hourly backups');
    expect(describeBackupSchedule('15 * * * *')).toBe('Hourly backups at 15 minutes past');
  });

  test('weekly', () => {
    expect(describeBackupSchedule('0 3 * * 0')).toBe('Weekly backups on Sunday at 03:00 UTC');
    expect(describeBackupSchedule('0 3 * * 7')).toBe('Weekly backups on Sunday at 03:00 UTC');
  });

  test('anything else never prints the raw cron', () => {
    for (const cron of ['0 2 * * 1-5', '*/5 * * * *', '0 0 1 * *', 'garbage', '', undefined]) {
      expect(describeBackupSchedule(cron)).toBe('Scheduled backups');
    }
  });
});
