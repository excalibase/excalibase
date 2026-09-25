import { describe, test, expect } from 'vitest';
import { appDisplayStatus, describeTier, plainFailureReason, suggestAppName } from './appCopy';
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
  ])('%#', (a, d, label) => {
    expect(appDisplayStatus(a, d).label).toBe(label);
  });
});

describe('suggestAppName', () => {
  test.each([
    ['nginx:1.27', 'nginx'],
    ['ghcr.io/acme/My_Web:1.0', 'my-web'],
    ['localhost:5000/api@sha256:abc', 'api'],
    ['', ''],
    ['x:1', ''],
  ])('%s → %s', (image, name) => {
    expect(suggestAppName(image)).toBe(name);
  });
});
