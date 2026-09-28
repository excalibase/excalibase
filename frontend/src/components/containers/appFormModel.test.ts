import { describe, test, expect } from 'vitest';
import { initialValues, toAppSubmission, validateAppForm } from './appFormModel';

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
