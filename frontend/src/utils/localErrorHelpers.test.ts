import { describe, test, expect } from 'vitest';
import { AxiosError } from 'axios';
import { apiErrorMessage as permissionsMessage } from '../api/permissions';
import { refusalMessage } from '../api/clusterSettings';
import { apiErrorMessage as documentsMessage } from '../api/documents';
import { importErrorOf } from '../api/tableImport';

// Every area's own helper reads the server's reason the same way: never
// axios's "Request failed with status code N".
const noReason = new AxiosError('Request failed with status code 502', 'ERR_BAD_RESPONSE', undefined, undefined, {
  status: 502, data: '<html>Bad Gateway</html>',
} as never);
const unreachable = new AxiosError('Network Error', 'ERR_NETWORK');
const withReason = new AxiosError('Request failed with status code 400', 'ERR_BAD_REQUEST', undefined, undefined, {
  status: 400, data: { error: 'max_connections must be at most 100' },
} as never);

const helpers: Array<[string, (err: unknown) => string]> = [
  ['permissions', (err) => permissionsMessage(err, 'Could not save')],
  ['cluster settings', refusalMessage],
  ['documents', documentsMessage],
  ['table import', (err) => importErrorOf(err).message],
];

describe.each(helpers)('%s error text', (_name, message) => {
  test("keeps the server's reason", () => {
    expect(message(withReason)).toBe('max_connections must be at most 100');
  });

  test('never shows the status-code text', () => {
    expect(message(noReason)).not.toMatch(/Request failed with status code/);
    expect(message(noReason)).toMatch(/server hit an error/);
  });

  test('says when the server could not be reached', () => {
    expect(message(unreachable)).toMatch(/could not be reached/);
  });
});
