import { describe, test, expect } from 'vitest';
import { validateAppForm } from './appFormModel';

const base = { name: 'web', image: 'nginx:1.27', port: '8080', replicas: '1', healthCheckPath: '', env: [] };

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
