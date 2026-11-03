import { describe, test, expect, vi, beforeEach } from 'vitest';
import { api } from './client';
import {
  getAlertSettings,
  getContainerHistory,
  getHostMonitoring,
  getMonitoredContainers,
  getProjectContainerMetrics,
  saveAlertSettings,
  sendTestAlert,
} from './monitoring';

vi.mock('./client', () => ({ api: { get: vi.fn(), put: vi.fn(), post: vi.fn() } }));

describe('monitoring api', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({ data: { ok: 'get' } } as never);
    vi.mocked(api.put).mockResolvedValue({ data: { ok: 'put' } } as never);
    vi.mocked(api.post).mockResolvedValue({ data: { ok: 'post' } } as never);
  });

  test('host monitoring is read for the asked range', async () => {
    expect(await getHostMonitoring('6h')).toEqual({ ok: 'get' });
    expect(api.get).toHaveBeenCalledWith('/admin/monitoring/host?range=6h');
  });

  test('the containers snapshot is read', async () => {
    await getMonitoredContainers();
    expect(api.get).toHaveBeenCalledWith('/admin/monitoring/containers');
  });

  test("a container's history escapes its name", async () => {
    await getContainerHistory('excalibase/app one', '7d');
    expect(api.get).toHaveBeenCalledWith(
      '/admin/monitoring/containers/excalibase%2Fapp%20one?range=7d',
    );
  });

  test('alert settings are read, saved and tested', async () => {
    await getAlertSettings();
    expect(api.get).toHaveBeenCalledWith('/admin/monitoring/alert-settings');

    const body = {
      enabled: true,
      cpuPercent: 90,
      memoryPercent: null,
      diskPercent: null,
      temperatureCelsius: null,
      load5: null,
      networkMbps: null,
      windowMinutes: 5,
      downMinutes: 3,
      emails: [],
    };
    expect(await saveAlertSettings(body)).toEqual({ ok: 'put' });
    expect(api.put).toHaveBeenCalledWith('/admin/monitoring/alert-settings', body);

    expect(await sendTestAlert()).toEqual({ ok: 'post' });
    expect(api.post).toHaveBeenCalledWith('/admin/monitoring/alert-settings/test');
  });

  test("a project's containers are read under the project", async () => {
    await getProjectContainerMetrics('proj 1', '24h');
    expect(api.get).toHaveBeenCalledWith('/projects/proj%201/containers/metrics?range=24h');
  });
});
