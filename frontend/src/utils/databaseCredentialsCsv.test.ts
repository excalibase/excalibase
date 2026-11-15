import { describe, test, expect } from 'vitest';
import { credentialsCsv, credentialsCsvFileName } from './databaseCredentialsCsv';

const base = { host: 'p1.db.excalibase.io', port: '26257', database: 'appdb', user: 'app', password: 'k8Xq2Lm9Zp4Rt7Vw', sslmode: 'verify-full' };

describe('credentialsCsv', () => {
  test('has the header row and one data row', () => {
    expect(credentialsCsv(base)).toBe(
      'host,port,database,user,password,sslmode\n"p1.db.excalibase.io","26257","appdb","app","k8Xq2Lm9Zp4Rt7Vw","verify-full"\n',
    );
  });

  test('quotes commas, quotes and newlines', () => {
    const lines = credentialsCsv({ ...base, password: 'a,"b"\nc' });
    expect(lines).toContain('"a,""b""\nc"');
  });

  test('keeps the password as typed even when it starts with a formula character', () => {
    for (const lead of ['=', '+', '-', '@']) {
      expect(credentialsCsv({ ...base, password: `${lead}cmd` })).toContain(`"${lead}cmd"`);
    }
  });

  test('leaves a harmless value untouched', () => {
    expect(credentialsCsv(base)).not.toContain("'");
  });
});

describe('credentialsCsvFileName', () => {
  test('names the file after the project', () => {
    expect(credentialsCsvFileName('my-proj')).toBe('my-proj-database-credentials.csv');
  });

  test('strips characters that are unsafe in a file name', () => {
    expect(credentialsCsvFileName('../a b')).toBe('a-b-database-credentials.csv');
  });
});

describe('credentialsCsv password fidelity', () => {
  test('writes a password that starts with a formula character exactly as chosen', () => {
    const csv = credentialsCsv({ host: 'h', port: '5432', database: 'd', user: 'u', password: '=passphrase-with-dash', sslmode: 'require' });
    expect(csv).toContain(',"=passphrase-with-dash",');
  });
});
