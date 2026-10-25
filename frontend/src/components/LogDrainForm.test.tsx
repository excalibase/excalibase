import { describe, test, expect } from 'vitest';
import {
  REENTER_HEADER_VALUE,
  createBody,
  drainProblem,
  emptyDrainValues,
  needsHeaderValueAgain,
  updateBody,
  valuesOf,
  type DrainValues,
} from './LogDrainForm';
import { drainDestination, httpsUrlProblem, type LogDrain } from '../api/logDrains';

const https = (overrides: Partial<DrainValues> = {}): DrainValues => ({
  ...emptyDrainValues,
  name: 'collector',
  kind: 'https',
  url: 'https://logs.example.com/in',
  ...overrides,
});

const withHeader: LogDrain = {
  id: 'ld_1',
  name: 'collector',
  kind: 'https',
  url: 'https://logs.example.com/...',
  headerName: 'Authorization',
  hasSecret: true,
  enabled: true,
  status: 'active',
  consecutiveFailures: 0,
  sentLines: 0,
  droppedLines: 0,
};

const editOf = (overrides: Partial<DrainValues> = {}): DrainValues => ({
  ...valuesOf(withHeader),
  ...overrides,
});

// EXC-658: the client mirrors the server's drain rules before sending.
describe('log drain form rules', () => {
  test('a name is required', () => {
    expect(drainProblem({ ...emptyDrainValues, secret: 'k' })).toMatch(/name/);
  });

  test('a stored Datadog key need not be typed again', () => {
    const datadog = { ...withHeader, kind: 'datadog', datadogSite: 'us1' } as LogDrain;
    expect(drainProblem({ ...emptyDrainValues, name: 'dd' }, datadog)).toBeUndefined();
    expect(updateBody({ ...emptyDrainValues, name: 'dd', secret: 'k2' }, datadog)).toEqual({
      name: 'dd',
      datadogSite: 'us1',
      secret: 'k2',
    });
  });

  test('an HTTPS drain may have no header at all', () => {
    expect(drainProblem(https())).toBeUndefined();
    expect(createBody(https())).toEqual({
      name: 'collector',
      kind: 'https',
      url: 'https://logs.example.com/in',
    });
  });

  test('a value without a header name is refused', () => {
    expect(drainProblem(https({ secret: 'v' }))).toMatch(/both/);
    expect(drainProblem(editOf({ headerName: '', secret: 'v' }), withHeader)).toMatch(/both/);
  });

  test('the URL must parse and use port 443', () => {
    expect(httpsUrlProblem('https://')).toMatch(/not valid/);
    expect(httpsUrlProblem('https://logs.example.com:443/in')).toBeUndefined();
    expect(httpsUrlProblem('ftp://x')).toMatch(/https:\/\//);
    expect(drainProblem(editOf({ url: 'http://x' }), withHeader)).toMatch(/https:\/\//);
  });

  test('the edit form starts with an empty URL, not the masked one', () => {
    expect(valuesOf(withHeader).url).toBe('');
  });

  test('an untouched edit sends neither URL, header nor secret', () => {
    expect(drainProblem(editOf(), withHeader)).toBeUndefined();
    expect(updateBody(editOf(), withHeader)).toEqual({ name: 'collector' });
  });

  test('a new host with a stored header needs the value again', () => {
    const moved = editOf({ url: 'https://other.example.net/in' });
    expect(needsHeaderValueAgain(moved, withHeader)).toBe(true);
    expect(drainProblem(moved, withHeader)).toBe(REENTER_HEADER_VALUE);
    const withValue = { ...moved, secret: 'Bearer new' };
    expect(drainProblem(withValue, withHeader)).toBeUndefined();
    expect(updateBody(withValue, withHeader)).toEqual({
      name: 'collector',
      url: 'https://other.example.net/in',
      secret: 'Bearer new',
    });
  });

  test('a new path on the same host keeps the stored value', () => {
    const samehost = editOf({ url: 'https://LOGS.example.com/v2' });
    expect(needsHeaderValueAgain(samehost, withHeader)).toBe(false);
    expect(updateBody(samehost, withHeader)).toEqual({
      name: 'collector',
      url: 'https://LOGS.example.com/v2',
    });
  });

  test('a renamed header needs the value again', () => {
    const renamed = editOf({ headerName: 'X-Api-Key' });
    expect(drainProblem(renamed, withHeader)).toBe(REENTER_HEADER_VALUE);
    expect(updateBody({ ...renamed, secret: 'k' }, withHeader)).toEqual({
      name: 'collector',
      headerName: 'X-Api-Key',
      secret: 'k',
    });
  });

  test('clearing the header never sends a secret', () => {
    const cleared = editOf({ headerName: '' });
    expect(drainProblem(cleared, withHeader)).toBeUndefined();
    expect(updateBody(cleared, withHeader)).toEqual({ name: 'collector', headerName: '' });
    expect(updateBody({ ...cleared, secret: 'stray' }, withHeader)).not.toHaveProperty('secret');
  });

  test('adding a header to a drain without one needs its value', () => {
    const bare = { ...withHeader, headerName: undefined, hasSecret: false };
    expect(drainProblem(editOf({ headerName: 'Authorization' }), bare)).toMatch(/both/);
    expect(needsHeaderValueAgain(editOf({ headerName: 'X' }), bare)).toBe(false);
  });

  test('an unknown Datadog site is still named', () => {
    const drain = { kind: 'datadog', datadogSite: 'zz9' } as LogDrain;
    expect(drainDestination(drain)).toBe('Datadog zz9');
    expect(drainDestination({ kind: 'https' } as LogDrain)).toBe('');
  });
});
