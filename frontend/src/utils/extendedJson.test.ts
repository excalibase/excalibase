import { describe, expect, test } from 'vitest';
import { EJSON } from 'bson';
import { toEditable } from './extendedJson';

describe('toEditable', () => {
  test('writes plainly what reads back as the same type, and keeps the rest explicit', () => {
    const canonical = {
      _id: { $oid: '65f000000000000000000001' },
      int: { $numberInt: '5' },
      wholeDouble: { $numberDouble: '5.0' },
      double: { $numberDouble: '1.5' },
      smallLong: { $numberLong: '7' },
      bigLong: { $numberLong: '3000000000' },
      hugeLong: { $numberLong: '9007199254740993' },
      date: { $date: { $numberLong: '1704067200000' } },
      nested: { list: [{ $numberInt: '1' }, 's', { deep: { $numberDouble: '2.5' } }] },
      nan: { $numberDouble: 'NaN' },
    };
    expect(toEditable(canonical)).toEqual({
      _id: { $oid: '65f000000000000000000001' },
      int: 5,
      wholeDouble: { $numberDouble: '5.0' },
      double: 1.5,
      smallLong: { $numberLong: '7' },
      bigLong: 3000000000,
      hugeLong: { $numberLong: '9007199254740993' },
      date: { $date: '2024-01-01T00:00:00.000Z' },
      nested: { list: [1, 's', { deep: 2.5 }] },
      nan: { $numberDouble: 'NaN' },
    });
  });

  test('round-trips every type through Extended JSON unchanged', () => {
    const canonical = {
      a: { $numberInt: '5' },
      b: { $numberDouble: '5.0' },
      c: { $numberLong: '7' },
      d: { $numberLong: '3000000000' },
      e: { $numberDouble: '1.5' },
      f: { $date: { $numberLong: '1704067200000' } },
    };
    const edited = JSON.stringify(toEditable(canonical));
    const back = EJSON.stringify(EJSON.parse(edited, { relaxed: false }), { relaxed: false });
    expect(JSON.parse(back)).toEqual(canonical);
  });

  test('keeps dates outside what an ISO string can say', () => {
    expect(toEditable({ $date: { $numberLong: '-1000' } })).toEqual({ $date: { $numberLong: '-1000' } });
  });
});
