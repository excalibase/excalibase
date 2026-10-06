import { describe, test, expect } from 'vitest';
import {
  appDisplayStatus,
  describeTier,
  diskStopReason,
  isAppRunning,
  plainFailureReason,
  suggestAppName,
} from './appCopy';
import type { App, Deploy } from '../../api/apps';

describe('plainFailureReason', () => {
  test.each([
    ['app rollout: web ImagePullBackOff: Back-off pulling image', /image could not be pulled/i],
    ['app rollout: web ErrImagePull: not found', /image could not be pulled/i],
    ['app rollout: web InvalidImageName: bad', /image name is not valid/i],
    ['app rollout: web CrashLoopBackOff: back-off restarting', /keeps crashing/i],
    ['app rollout: web CreateContainerConfigError: secret "x" not found', /could not be set up/i],
    ['app rollout: web did not become ready within 5m0s', /did not become ready in time/i],
    ['app rollout: web Unschedulable: no node can run runtime class "gvisor"', /no room to run/i],
    ['the project has no namespace to deploy into', /not ready to run containers/i],
    ['there is no room to run the app right now; try again later', /no room to run/i],
    [
      'read the pull credential for ghcr.io: the credential store could not be read',
      /credential for the image's registry could not be read/i,
    ],
    [
      'render app workload: resolve secret for "API_KEY": not found',
      /secret variable could not be read/i,
    ],
    [
      'render app workload: resolve "DATABASE_URL" (database appdb.DATABASE_URL): gone',
      /database variable could not be resolved/i,
    ],
    [
      'render app workload: resolve secret for "API_KEY": no value is stored',
      /has no value stored/i,
    ],
    [
      "the app's disk holds more than the plan allows: it holds 1200Mi and the plan allows 1Gi; the app was stopped",
      /disk holds more than the plan allows.*stopped/i,
    ],
    [
      'render app workload: "API_KEY" is a secret and no resolver was given',
      /cannot supply database or secret variables/i,
    ],
  ])('%s', (raw, expected) => {
    expect(plainFailureReason(raw)).toMatch(expected);
  });

  test('falls back to a generic sentence rather than inventing a cause', () => {
    expect(plainFailureReason('something odd')).toBe('The deploy failed.');
    expect(plainFailureReason(undefined)).toBe('The deploy failed.');
  });
});

describe('describeTier', () => {
  test('describes each size in plain words with its replica cap', () => {
    expect(describeTier('FREE')).toMatchObject({ label: 'Small', maxReplicas: 1 });
    expect(describeTier('STANDARD')).toMatchObject({ label: 'Medium', maxReplicas: 3 });
    expect(describeTier('ENTERPRISE')).toMatchObject({ label: 'Large', maxReplicas: 3 });
  });
});

describe('appDisplayStatus', () => {
  const app = { replicas: 1 } as App;
  const withStatus = (status: Deploy['status']) => ({ status }) as Deploy;

  test.each([
    [app, undefined, 'Not deployed'],
    [{ replicas: 0 } as App, withStatus('succeeded'), 'Stopped'],
    [app, withStatus('pending'), 'Deploying'],
    [app, withStatus('rolling'), 'Deploying'],
    [app, withStatus('succeeded'), 'Running'],
    [app, withStatus('failed'), 'Failed'],
    [{ replicas: 1, status: 'PAUSED' } as App, withStatus('succeeded'), 'Paused'],
    [{ replicas: 1, status: 'PAUSING' } as App, withStatus('succeeded'), 'Pausing'],
    [{ replicas: 1, status: 'RESUMING' } as App, withStatus('succeeded'), 'Resuming'],
    [{ replicas: 1, status: 'DELETING' } as App, withStatus('succeeded'), 'Deleting'],
  ])('%#', (a, d, label) => {
    expect(appDisplayStatus(a, d).label).toBe(label);
  });

  test('only a queued or rolling deploy spins; an unknown one reads Unknown', () => {
    expect(appDisplayStatus(app, withStatus('pending')).tone).toBe('progress');
    expect(appDisplayStatus(app, withStatus('rolling')).tone).toBe('progress');
    expect(appDisplayStatus(app, withStatus('superseded'))).toEqual({ label: 'Replaced by a newer deploy', tone: 'neutral' });
    expect(appDisplayStatus(app, withStatus('exploded' as Deploy['status']))).toEqual({ label: 'Unknown (exploded)', tone: 'neutral' });
  });
});

describe('diskStopReason', () => {
  const reason =
    "the app's disk holds more than the plan allows: it holds 1200Mi and the plan allows 1Gi; the app was stopped";
  const paused = { status: 'PAUSED' } as App;
  const failed = (failureReason?: string) => ({ status: 'failed', failureReason }) as Deploy;

  test('names why a stopped app was stopped for its disk', () => {
    expect(diskStopReason(paused, failed(reason))).toBe(reason);
  });

  test.each([
    [{ status: 'ACTIVE' } as App, failed(reason)],
    [paused, failed('app rollout: web CrashLoopBackOff')],
    [paused, { status: 'succeeded', failureReason: reason } as Deploy],
    [paused, undefined],
  ])('is absent otherwise %#', (a, d) => {
    expect(diskStopReason(a, d)).toBeUndefined();
  });
});

describe('isAppRunning', () => {
  test.each([
    ['ACTIVE', true],
    ['PAUSING', true],
    ['RESUMING', true],
    ['DELETING', true],
    ['PAUSED', false],
    ['FAILED', false],
    ['PROVISIONING', false],
  ])('%s is running: %s', (status, running) => {
    expect(isAppRunning({ status } as App)).toBe(running);
  });
});

describe('suggestAppName', () => {
  test.each([
    ['nginx:1.27', 'nginx'],
    ['ghcr.io/acme/My_Web:1.0', 'my-web'],
    ['localhost:5000/api@sha256:abc', 'api'],
    ['', ''],
    ['x:1', ''],
    ['acme/9lives:1', ''],
    ['acme/proj-tool:1', ''],
  ])('%s → %s', (image, name) => {
    expect(suggestAppName(image)).toBe(name);
  });
});
