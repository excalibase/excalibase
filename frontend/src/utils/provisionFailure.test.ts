import { describe, test, expect } from 'vitest';
import { GENERIC_PROVISION_FAILURE, PROVISION_FAILURE_MESSAGES, plainProvisionFailure } from './provisionFailure';

describe('plainProvisionFailure', () => {
  test.each([
    ['container db OOMKilled (exit 137)', 'ran out of memory'],
    ['pod db-1 was OOMKilled', 'ran out of memory'],
    ['persistentvolumeclaim "data-db-1" not found', 'storage could not be attached'],
    ['PVC data-1 is pending', 'storage could not be attached'],
    ['FailedAttachVolume: Multi-Attach error', 'storage could not be attached'],
    ['db: ImagePullBackOff: back-off pulling image "pg:99"', 'image could not be downloaded'],
    ['ErrImagePull', 'image could not be downloaded'],
    ['pod is in CrashLoopBackOff', 'kept crashing'],
    ['0/3 nodes are available: Insufficient memory. Unschedulable', 'no room'],
    ['timed out waiting for the cluster to become ready', 'took too long'],
    ['context deadline exceeded', 'took too long'],
  ])('translates %s', (raw, wording) => {
    expect(plainProvisionFailure(raw).toLowerCase()).toContain(wording);
  });

  test('the first matching rule wins, specific before broad', () => {
    expect(plainProvisionFailure('OOMKilled then CrashLoopBackOff').toLowerCase()).toContain('ran out of memory');
  });

  test.each([undefined, '', '   ', 'cluster rejected', 'pq: relation "x" does not exist', 'k8s says boom pod-7'])(
    'an unknown or empty text (%s) gives the generic wording and never passes through',
    (raw) => {
      const out = plainProvisionFailure(raw);
      expect(out).toBe(GENERIC_PROVISION_FAILURE);
      expect(out).toBe('Provisioning failed. Try again or contact support.');
    },
  );

  test('a known pattern never echoes the raw text either', () => {
    const raw = 'pod db-7f9 on node ip-10-0-0-5 OOMKilled';
    const out = plainProvisionFailure(raw);
    expect(out).not.toContain('db-7f9');
    expect(out).not.toContain('ip-10');
  });

  test('every wording is a plain sentence', () => {
    for (const message of [GENERIC_PROVISION_FAILURE, ...PROVISION_FAILURE_MESSAGES]) {
      expect(message).toMatch(/^[A-Z].*\.$/);
    }
  });
});
