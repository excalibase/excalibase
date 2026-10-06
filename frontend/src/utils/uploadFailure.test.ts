import { describe, test, expect } from 'vitest';
import { AxiosError, AxiosHeaders } from 'axios';
import { storageRefusal, uploadFailure, UploadStepError } from './uploadFailure';

function axiosFailure(status: number | undefined, data?: unknown): AxiosError {
  const config = { headers: new AxiosHeaders() };
  const response = status === undefined ? undefined : { status, statusText: '', headers: {}, config, data };
  return new AxiosError(`Request failed with status code ${status}`, 'ERR_BAD_RESPONSE', config, {}, response as never);
}

const S3_EXPIRED = `<?xml version="1.0" encoding="UTF-8"?>
<Error><Code>AccessDenied</Code><Message>Request has expired</Message><RequestId>abc</RequestId></Error>`;

describe('storageRefusal', () => {
  test('reads the S3 error code and message', () => {
    expect(storageRefusal(S3_EXPIRED)).toBe('AccessDenied: Request has expired');
  });

  test('a code without a message is still told', () => {
    expect(storageRefusal('<Error><Code>SignatureDoesNotMatch</Code></Error>')).toBe('SignatureDoesNotMatch');
  });

  test('entities in the message are decoded', () => {
    expect(storageRefusal('<Error><Code>InvalidArgument</Code><Message>a &lt;b&gt; &amp; c</Message></Error>'))
      .toBe('InvalidArgument: a <b> & c');
  });

  test('anything that is not an S3 error body gives nothing', () => {
    for (const body of [undefined, '', '<html>Forbidden</html>', { error: 'x' }, 42]) {
      expect(storageRefusal(body)).toBeNull();
    }
  });
});

describe('uploadFailure', () => {
  test('a storage refusal names the step, the HTTP status and the S3 reason', () => {
    const err = uploadFailure('put', axiosFailure(403, S3_EXPIRED));
    expect(err).toBeInstanceOf(UploadStepError);
    expect(err.status).toBe(403);
    expect(err.message).toBe('while sending the file to storage: HTTP 403, AccessDenied: Request has expired');
  });

  test('a storage refusal with no XML still gives the status', () => {
    expect(uploadFailure('put', axiosFailure(500, '')).message).toBe('while sending the file to storage: HTTP 500');
  });

  test('a request the browser blocked (CORS or network) says the storage could not be reached', () => {
    expect(uploadFailure('put', axiosFailure(undefined)).message)
      .toBe('while sending the file to storage: the storage service could not be reached (a network or CORS refusal)');
  });

  test("an API step gives the server's reason and status", () => {
    expect(uploadFailure('sign', axiosFailure(400, { error: 'mime type "text/html" not allowed in bucket' })).message)
      .toBe('while getting an upload URL: HTTP 400, mime type "text/html" not allowed in bucket');
    expect(uploadFailure('confirm', axiosFailure(502, '')).message)
      .toBe('while confirming the upload: HTTP 502');
  });

  test('an API step the server never answered says so', () => {
    expect(uploadFailure('confirm', axiosFailure(undefined)).message)
      .toBe('while confirming the upload: the server could not be reached');
  });

  test('a failure that is not an HTTP error keeps its own text', () => {
    expect(uploadFailure('sign', new Error('boom')).message).toBe('while getting an upload URL: boom');
    expect(uploadFailure('sign', 'odd').message).toBe('while getting an upload URL: no reason given');
  });
});
