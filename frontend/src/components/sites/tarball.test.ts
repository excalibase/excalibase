import { gunzipSync } from 'node:zlib';
import { describe, expect, test } from 'vitest';
import { packFolder, packTar, sitePathsFromFolder, type TarEntry } from './tarball';

const text = (value: string) => new TextEncoder().encode(value);
const decode = (bytes: Uint8Array) => new TextDecoder().decode(bytes);

// Reads back the ustar archive packTar writes: name, prefix, size, typeflag, body.
function readTar(archive: Uint8Array) {
  const entries: Array<{ path: string; body: string; type: string; checksumOk: boolean }> = [];
  let offset = 0;
  while (offset + 512 <= archive.length) {
    const header = archive.subarray(offset, offset + 512);
    if (header.every((byte) => byte === 0)) break;
    const field = (start: number, length: number) =>
      decode(header.subarray(start, start + length)).split('\u0000')[0];
    const size = Number.parseInt(field(124, 12).trim(), 8);
    const prefix = field(345, 155);
    const name = field(0, 100);
    let sum = 0;
    header.forEach((byte, index) => {
      sum += index >= 148 && index < 156 ? 32 : byte;
    });
    entries.push({
      path: prefix ? `${prefix}/${name}` : name,
      body: decode(archive.subarray(offset + 512, offset + 512 + size)),
      type: field(156, 1),
      checksumOk: Number.parseInt(field(148, 8).trim(), 8) === sum,
    });
    offset += 512 + Math.ceil(size / 512) * 512;
  }
  return {
    entries,
    trailerZeroed: archive.subarray(offset).every((byte) => byte === 0),
    length: archive.length,
  };
}

describe('packTar', () => {
  test('writes each file as a regular ustar entry with a valid checksum', () => {
    const entries: TarEntry[] = [
      { path: 'index.html', data: text('<h1>hi</h1>') },
      { path: 'assets/app.js', data: text('console.log(1)') },
    ];
    const archive = readTar(packTar(entries));
    expect(archive.entries).toEqual([
      { path: 'index.html', body: '<h1>hi</h1>', type: '0', checksumOk: true },
      { path: 'assets/app.js', body: 'console.log(1)', type: '0', checksumOk: true },
    ]);
    expect(archive.length % 512).toBe(0);
    expect(archive.trailerZeroed).toBe(true);
  });

  test('splits a path longer than 100 bytes into the ustar prefix', () => {
    const deep = `${'d'.repeat(80)}/${'e'.repeat(60)}/file.js`;
    const archive = readTar(packTar([{ path: deep, data: text('x') }]));
    expect(archive.entries[0].path).toBe(deep);
    expect(archive.entries[0].checksumOk).toBe(true);
  });

  test('refuses a path no ustar header can hold', () => {
    const tooDeep = `${'d'.repeat(200)}/${'f'.repeat(120)}`;
    expect(() => packTar([{ path: tooDeep, data: text('x') }])).toThrow(/too long/);
  });
});

describe('sitePathsFromFolder', () => {
  test('drops the picked folder itself so index.html sits at the root', () => {
    expect(sitePathsFromFolder(['dist/index.html', 'dist/assets/a.js'])).toEqual([
      'index.html',
      'assets/a.js',
    ]);
  });

  test('keeps paths that share no single top folder', () => {
    expect(sitePathsFromFolder(['index.html', 'a/b.js'])).toEqual(['index.html', 'a/b.js']);
  });
});

describe('packFolder', () => {
  const picked = (path: string, body: string) => {
    const file = new File([body], path.split('/').pop() ?? path);
    Object.defineProperty(file, 'webkitRelativePath', { value: path });
    return file;
  };

  test('gzips the folder with its own name dropped from every path', async () => {
    const blob = await packFolder([
      picked('dist/index.html', '<h1>hi</h1>'),
      picked('dist/a/b.js', 'b'),
    ]);
    const bytes = new Uint8Array(await blob.arrayBuffer());
    expect(blob.type).toBe('application/gzip');
    expect([bytes[0], bytes[1]]).toEqual([0x1f, 0x8b]);
    const plain = new Uint8Array(gunzipSync(bytes));
    expect(readTar(plain).entries.map((entry) => entry.path)).toEqual(['index.html', 'a/b.js']);
  });
});
