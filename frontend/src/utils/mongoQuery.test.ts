import { describe, expect, test } from 'vitest';
import { EditorState } from '@codemirror/state';
import { CompletionContext, type CompletionResult } from '@codemirror/autocomplete';
import {
  fieldPaths,
  mongoCompletionSource,
  parseDocumentText,
  toServerQuery,
  documentIdParam,
} from './mongoQuery';

describe('toServerQuery', () => {
  test('turns shell syntax into canonical Extended JSON', () => {
    const result = toServerQuery({
      filter: '{total: {$gt: 5}, _id: ObjectId("65f000000000000000000001")}',
      sort: '{total: -1}',
      projection: '{total: 1}',
    });
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(JSON.parse(result.value.filter!)).toEqual({
      total: { $gt: { $numberInt: '5' } },
      _id: { $oid: '65f000000000000000000001' },
    });
    expect(JSON.parse(result.value.sort!)).toEqual({ total: { $numberInt: '-1' } });
    expect(JSON.parse(result.value.projection!)).toEqual({ total: { $numberInt: '1' } });
  });

  test('leaves empty parts out', () => {
    const result = toServerQuery({ filter: '  ', sort: '', projection: '{}' });
    expect(result).toEqual({ ok: true, value: {} });
  });

  test('names the part that does not parse', () => {
    const result = toServerQuery({ filter: '{}', sort: '{a: ', projection: '' });
    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.part).toBe('sort');
    expect(result.error).toMatch(/sort/i);
  });

  test('rejects a filter that is not a document', () => {
    const result = toServerQuery({ filter: '[1, 2]', sort: '', projection: '' });
    expect(result.ok).toBe(false);
  });
});

describe('fieldPaths', () => {
  test('lists dotted paths through nested documents and arrays of documents', () => {
    const paths = fieldPaths([
      { _id: { $oid: 'x' }, total: 5, customer: { name: 'a', address: { city: 'b' } } },
      { tags: ['a'], items: [{ sku: 'x', qty: 1 }], created: { $date: '2024-01-01' } },
    ]);
    expect(paths).toEqual([
      '_id',
      'created',
      'customer',
      'customer.address',
      'customer.address.city',
      'customer.name',
      'items',
      'items.qty',
      'items.sku',
      'tags',
      'total',
    ]);
  });

  test('ignores anything that is not a document', () => {
    expect(fieldPaths([null, 5, 'x', [1]])).toEqual([]);
  });
});

function complete(doc: string, operators: boolean, fields: string[]): CompletionResult | null {
  const state = EditorState.create({ doc });
  const source = mongoCompletionSource(() => fields, operators);
  return source(new CompletionContext(state, doc.length, true)) as CompletionResult | null;
}

describe('mongoCompletionSource', () => {
  test('offers field paths, quoting dotted ones', () => {
    const result = complete('{cus', true, ['customer', 'customer.name']);
    expect(result?.from).toBe(1);
    const labels = result?.options.map((o) => o.label);
    expect(labels).toEqual(['customer', 'customer.name']);
    expect(result?.options[1].apply).toBe('"customer.name"');
    expect(result?.options[0].apply).toBeUndefined();
  });

  test('offers query operators after a dollar sign', () => {
    const result = complete('{total: {$g', true, ['total']);
    const labels = result?.options.map((o) => o.label) ?? [];
    expect(labels).toContain('$gt');
    expect(labels).toContain('$gte');
    expect(labels).not.toContain('total');
  });

  test('offers no operators where they do not belong', () => {
    const result = complete('{$', false, ['total']);
    expect(result).toBeNull();
  });

  test('offers nothing when nothing is typed and completion was not asked for', () => {
    const state = EditorState.create({ doc: '{ ' });
    const source = mongoCompletionSource(() => ['a'], true);
    expect(source(new CompletionContext(state, 2, false))).toBeNull();
  });
});

describe('parseDocumentText', () => {
  test('accepts a JSON object', () => {
    expect(parseDocumentText('{"a": {"$oid": "x"}}')).toEqual({ ok: true });
  });

  test.each([['[1]'], ['5'], ['{a: 1}'], ['']])('rejects %s', (text) => {
    expect(parseDocumentText(text).ok).toBe(false);
  });
});

describe('documentIdParam', () => {
  test('is the _id as Extended JSON', () => {
    expect(documentIdParam({ _id: { $oid: 'abc' } })).toBe('{"$oid":"abc"}');
    expect(documentIdParam({ _id: 7 })).toBe('7');
    expect(documentIdParam({ a: 1 })).toBeNull();
  });
});
