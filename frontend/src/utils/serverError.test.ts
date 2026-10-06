import { describe, test, expect } from 'vitest';
import { AxiosError, AxiosHeaders } from 'axios';
import { serverErrorMessage } from './serverError';

function axiosFailure(status: number | undefined, data?: unknown): AxiosError {
  const config = { headers: new AxiosHeaders() };
  const response = status === undefined ? undefined : { status, statusText: '', headers: {}, config, data };
  return new AxiosError(`Request failed with status code ${status}`, 'ERR_BAD_RESPONSE', config, {}, response as never);
}

describe('serverErrorMessage', () => {
  test("shows the server's own reason", () => {
    expect(serverErrorMessage(axiosFailure(400, { error: 'project name must be 100 characters or fewer', status: 400 }), 'Could not create the project'))
      .toBe('project name must be 100 characters or fewer');
  });

  test('never shows axios\'s "Request failed with status code" text', () => {
    for (const status of [400, 404, 409, 500, 502]) {
      expect(serverErrorMessage(axiosFailure(status, ''), 'Could not save')).not.toMatch(/Request failed with status code/);
    }
  });

  test('a server error without a reason says the server failed, with the action', () => {
    expect(serverErrorMessage(axiosFailure(502, '<html>Bad Gateway</html>'), 'Could not save'))
      .toBe('Could not save: the server hit an error. Try again in a moment.');
  });

  test('a refusal without a reason falls back to the action', () => {
    expect(serverErrorMessage(axiosFailure(403, {}), 'Could not save')).toBe('Could not save');
  });

  test('no response at all means the server could not be reached', () => {
    expect(serverErrorMessage(axiosFailure(undefined), 'Could not save')).toBe('Could not save: the server could not be reached.');
  });

  test('our own errors keep their message', () => {
    expect(serverErrorMessage(new Error('Pick a PostgreSQL version first'), 'Could not save')).toBe('Pick a PostgreSQL version first');
  });

  test('anything else gets the fallback', () => {
    expect(serverErrorMessage('boom', 'Could not save')).toBe('Could not save');
    expect(serverErrorMessage(undefined, 'Could not save')).toBe('Could not save');
  });

  test('a JSON-looking reason is never shown raw', () => {
    expect(serverErrorMessage(axiosFailure(500, { error: { nested: true } }), 'Could not save'))
      .toBe('Could not save: the server hit an error. Try again in a moment.');
  });
});
