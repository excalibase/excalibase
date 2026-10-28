import { describe, test, expect } from 'vitest';
import { bodyFromForm, formFromSettings, parseEmails } from './alertSettingsForm';
import { alertSettings } from '../test/monitoringFixtures';

describe('alert settings form', () => {
  test('a saved setting fills the form, unset thresholds as empty', () => {
    const form = formFromSettings(alertSettings());
    expect(form).toMatchObject({
      enabled: true,
      cpuPercent: '90',
      memoryPercent: '85',
      diskPercent: '',
      windowMinutes: '5',
      downMinutes: '3',
      emails: 'ops@example.test',
      webhookInput: '',
      clearWebhook: false,
    });
  });

  test('an untouched webhook is kept by leaving it out', () => {
    const result = bodyFromForm(formFromSettings(alertSettings()));
    expect(result.error).toBeUndefined();
    expect(result.body).toEqual({
      enabled: true,
      cpuPercent: 90,
      memoryPercent: 85,
      diskPercent: null,
      temperatureCelsius: null,
      load5: null,
      networkMbps: null,
      windowMinutes: 5,
      downMinutes: 3,
      emails: ['ops@example.test'],
    });
    expect(result.body).not.toHaveProperty('webhookUrl');
  });

  test('a typed webhook replaces it and a cleared one is sent empty', () => {
    const form = formFromSettings(alertSettings());
    expect(
      bodyFromForm({ ...form, webhookInput: ' https://hooks.example.test/new ' }).body?.webhookUrl,
    ).toBe('https://hooks.example.test/new');
    expect(bodyFromForm({ ...form, clearWebhook: true }).body?.webhookUrl).toBe('');
  });

  test('emails split on commas and lines, at most ten', () => {
    expect(parseEmails('a@x.test, b@x.test\n\nc@x.test ,')).toEqual([
      'a@x.test',
      'b@x.test',
      'c@x.test',
    ]);
    const eleven = Array.from({ length: 11 }, (_, n) => `u${n}@x.test`).join(',');
    expect(bodyFromForm({ ...formFromSettings(alertSettings()), emails: eleven }).error).toBe(
      'At most 10 email recipients.',
    );
  });

  test('a threshold or window that is not a number is refused before sending', () => {
    const form = formFromSettings(alertSettings());
    expect(bodyFromForm({ ...form, cpuPercent: 'lots' }).error).toBe('CPU % must be a number.');
    expect(bodyFromForm({ ...form, windowMinutes: '' }).error).toBe(
      'Window minutes must be a number.',
    );
    expect(bodyFromForm({ ...form, networkMbps: '-1' }).error).toBe(
      'Network Mbps must be 0 or more.',
    );
  });
});
