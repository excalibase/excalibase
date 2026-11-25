import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, test, expect } from 'vitest';
import { GENERIC_PROVISION_FAILURE, PROVISION_FAILURE_MESSAGES, plainProvisionFailure } from './provisionFailure';

describe('plainProvisionFailure', () => {
  // One row per alternative of each rule, each matching only that alternative.
  const [OOM, STORAGE, IMAGE, CRASH, ROOM, SLOW] = PROVISION_FAILURE_MESSAGES;
  test.each([
    ['OOMKilled', OOM],
    ['it ran out of memory', OOM],
    ['Out Of Memory', OOM],
    ['persistentvolumeclaim "data-1" not found', STORAGE],
    ['PVC data-1 is pending', STORAGE],
    ['FailedAttachVolume for data-1', STORAGE],
    ['FailedMount for data-1', STORAGE],
    ['Multi-Attach error', STORAGE],
    ['ImagePullBackOff', IMAGE],
    ['ErrImagePull', IMAGE],
    ['InvalidImageName', IMAGE],
    ['CrashLoopBackOff', CRASH],
    ['Unschedulable', ROOM],
    ['Insufficient cpu', ROOM],
    ['Insufficient memory', ROOM],
    ['0/3 nodes are available', ROOM],
    ['the database did not become ready', SLOW],
    ['timed out waiting for the database', SLOW],
    ['provisioning timed out', SLOW],
    ['provisioning timeout', SLOW],
    ['readiness deadline exceeded', SLOW],
  ])('%s gives its own wording', (raw, expected) => {
    expect(plainProvisionFailure(raw)).toBe(expected);
  });

  test('every wording says what to do next, with no support address', () => {
    for (const message of [GENERIC_PROVISION_FAILURE, ...PROVISION_FAILURE_MESSAGES]) {
      expect(message).toContain('Delete it and add it again. If it keeps failing, tell us.');
      expect(message).not.toMatch(/@|https?:|contact support/);
    }
  });

  test.each(['canceling statement due to lock timeout', 'etcdserver: request timed out', 'context deadline exceeded'])(
    'a timeout that is not about starting up (%s) stays generic',
    (raw) => {
      expect(plainProvisionFailure(raw)).toBe(GENERIC_PROVISION_FAILURE);
    },
  );

  test('the first matching rule wins, specific before broad', () => {
    expect(plainProvisionFailure('OOMKilled then CrashLoopBackOff').toLowerCase()).toContain('ran out of memory');
  });

  test.each([undefined, '', '   ', 'cluster rejected', 'pq: relation "x" does not exist', 'k8s says boom pod-7'])(
    'an unknown or empty text (%s) gives the generic wording and never passes through',
    (raw) => {
      const out = plainProvisionFailure(raw);
      expect(out).toBe(GENERIC_PROVISION_FAILURE);
      expect(out).toBe('Provisioning failed. Delete it and add it again. If it keeps failing, tell us.');
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

describe('plainProvisionFailure with the server kind', () => {
  const [OOM, STORAGE, IMAGE, CRASH, ROOM, SLOW] = PROVISION_FAILURE_MESSAGES;
  test.each([
    ['out_of_memory', OOM],
    ['storage', STORAGE],
    ['image', IMAGE],
    ['crashing', CRASH],
    ['no_capacity', ROOM],
    ['timeout', SLOW],
    ['unknown', GENERIC_PROVISION_FAILURE],
    ['a_kind_from_a_newer_server', GENERIC_PROVISION_FAILURE],
  ])('kind %s gives its own wording', (kind, expected) => {
    expect(plainProvisionFailure(undefined, kind)).toBe(expected);
  });

  test('the kind wins over the text, and the text is only the fallback', () => {
    expect(plainProvisionFailure('CrashLoopBackOff', 'storage')).toBe(STORAGE);
    expect(plainProvisionFailure('CrashLoopBackOff')).toBe(CRASH);
  });
});

// The server classifier reads the same file, so the two cannot drift apart.
describe('the shared failure-kind fixture', () => {
  const cases: Array<{ text: string; kind: string }> = JSON.parse(
    readFileSync(resolve(process.cwd(), '../shared-fixtures/failure-kinds.json'), 'utf8'),
  );
  const [OOM, STORAGE, IMAGE, CRASH, ROOM, SLOW] = PROVISION_FAILURE_MESSAGES;
  const byKind: Record<string, string> = {
    out_of_memory: OOM, storage: STORAGE, image: IMAGE, crashing: CRASH, no_capacity: ROOM, timeout: SLOW,
    unknown: GENERIC_PROVISION_FAILURE,
  };

  test('the text mapping and the kind mapping agree on every case', () => {
    expect(cases.length).toBeGreaterThan(20);
    for (const { text, kind } of cases) {
      expect(plainProvisionFailure(text), text).toBe(byKind[kind]);
      expect(plainProvisionFailure(undefined, kind), kind).toBe(byKind[kind]);
    }
  });
});
