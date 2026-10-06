import { describe, test, expect } from 'vitest';
import { emptyEnvRow, initialValues, toAppSubmission, validateAppForm } from './appFormModel';

const base = { ...initialValues(), name: 'web', image: 'nginx:1.27' };

// The name is the app's address inside its project (EXC-524), so it must be a
// Service name the platform does not already use.
describe('validateAppForm name', () => {
  test.each(['9web', 'web-', 'proj-api', 'deno-runtime', 'a'])('refuses %s', (name) => {
    expect(validateAppForm({ ...base, name }, 3).name).toBeDefined();
  });
  test.each(['web', 'redis', 'my-api-2'])('accepts %s', (name) => {
    expect(validateAppForm({ ...base, name }, 3).name).toBeUndefined();
  });
});

describe('internal TCP ports (EXC-525)', () => {
  test.each(['80', '1023', '65536', '8080', '6379, 6379', 'redis', '1,2,3', '2001,2002,2003,2004,2005,2006,2007,2008,2009'])(
    'refuses %s',
    (internalPorts) => {
      expect(validateAppForm({ ...base, internalPorts }, 3).internalPorts).toBeDefined();
    },
  );
  test.each(['', '6379', '6379, 9092', ' 5432 '])('accepts %s', (internalPorts) => {
    expect(validateAppForm({ ...base, internalPorts }, 3).internalPorts).toBeUndefined();
  });
  test('submits each as TCP, and an empty field clears them', () => {
    expect(toAppSubmission({ ...base, internalPorts: '6379, 9092' }).input.internalPorts).toEqual([
      { port: 6379, protocol: 'TCP' },
      { port: 9092, protocol: 'TCP' },
    ]);
    expect(toAppSubmission({ ...base, internalPorts: '' }).input.internalPorts).toEqual([]);
  });
  test('an edited app starts from its stored ports', () => {
    const app = { name: 'q', image: 'nats:2', port: 8222, replicas: 1, env: [], internalPorts: [{ port: 4222, protocol: 'TCP' as const }] };
    expect(initialValues(app as never).internalPorts).toBe('4222');
  });
});

describe('internal service (EXC-525)', () => {
  const service = { ...base, internal: true, port: '', healthCheckPath: '', internalPorts: '6379' };
  test('needs no HTTP port but at least one internal port', () => {
    const errors = validateAppForm(service, 3);
    expect(errors.port).toBeUndefined();
    expect(errors.internalPorts).toBeUndefined();
    expect(validateAppForm({ ...service, internalPorts: '' }, 3).internalPorts).toBeDefined();
  });
  test('submits no HTTP port and no health path', () => {
    const input = toAppSubmission({ ...service, healthCheckPath: '/stale' }).input;
    expect(input.internal).toBe(true);
    expect(input.port).toBe(0);
    expect(input.healthCheckPath).toBe('');
    expect(input.internalPorts).toEqual([{ port: 6379, protocol: 'TCP' }]);
  });
  test('a public web app is sent as not internal', () => {
    expect(toAppSubmission({ ...base, internalPorts: '' }).input.internal).toBe(false);
  });
  test('an edited internal service starts as one', () => {
    const app = { name: 'cache', image: 'redis:7', port: 0, internal: true, replicas: 1, env: [], internalPorts: [{ port: 6379, protocol: 'TCP' as const }] };
    const values = initialValues(app as never);
    expect(values.internal).toBe(true);
    expect(values.port).toBe('');
  });
});

// The server refuses a disk at or under a system directory (EXC-555), not only on it.
describe('disk mount path', () => {
  const withDisk = (diskMountPath: string) => ({ ...base, diskEnabled: true, diskMountPath, diskSize: '1', replicas: '1' });
  test.each(['/', '/usr', '/usr/local/data', '/usr/share/nginx/html', '/etc/app', '/bin/x', '/sbin/x', '/lib/x', '/lib64/x', '/boot/x', '/run/app', '/var', '/var/run/app', '/proc/x', '/sys/fs', '/dev/shm'])(
    'refuses %s',
    (path) => {
      expect(validateAppForm(withDisk(path), 3).diskMountPath).toBeDefined();
    },
  );
  test.each(['/data', '/var/lib/redis', '/var/lib/postgresql/data', '/home/app/.cache', '/usrdata', '/etcetera', '/running'])(
    'accepts %s',
    (path) => {
      expect(validateAppForm(withDisk(path), 3).diskMountPath).toBeUndefined();
    },
  );
});

// Checked before the app is created, so a set the server would refuse is never
// half-saved: the app written and its secrets refused one by one.
describe('environment set limits', () => {
  const literal = (index: number, value: string) => ({ ...emptyEnvRow(), id: index, name: `K${index}`, kind: 'literal' as const, value });
  const secret = (index: number) => ({ ...emptyEnvRow(), id: index, name: `S${index}`, kind: 'secret' as const, secretValue: 'v' });

  test('100 secrets fit: they count by number, not bytes', () => {
    const env = Array.from({ length: 100 }, (_, index) => secret(index));
    expect(validateAppForm({ ...base, env }, 3).env).toBeUndefined();
  });
  test('the 101st variable is refused on its own row', () => {
    const env = Array.from({ length: 101 }, (_, index) => secret(index));
    const errors = validateAppForm({ ...base, env }, 3).env ?? {};
    expect(errors[100]).toMatch(/at most 100/);
    expect(errors[99]).toBeUndefined();
  });
  test('plain values over 64 KB in total are refused where they cross it', () => {
    // Each row weighs its value, name and kind plus 64 bytes of framing, as the server counts it.
    const env = Array.from({ length: 9 }, (_, index) => literal(index, 'v'.repeat(8000)));
    const errors = validateAppForm({ ...base, env }, 3).env ?? {};
    expect(errors[8]).toMatch(/64 KB/);
    expect(errors[8]).toMatch(/secrets and references do not count/);
    expect(errors[7]).toBeUndefined();
  });
});
