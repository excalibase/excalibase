// Packs a picked build folder into a ustar archive in the browser, so a
// folder deploys the same way as an uploaded dist.tgz.
export interface TarEntry {
  readonly path: string;
  readonly data: Uint8Array;
}

const BLOCK = 512;
const encoder = new TextEncoder();

function writeField(block: Uint8Array, offset: number, length: number, value: string) {
  block.set(encoder.encode(value).subarray(0, length), offset);
}

function writeOctal(block: Uint8Array, offset: number, length: number, value: number) {
  writeField(block, offset, length, `${value.toString(8).padStart(length - 1, '0')}\0`);
}

// ustar keeps 100 bytes of name and 155 of prefix, split at a slash.
function splitPath(path: string): { name: string; prefix: string } {
  if (encoder.encode(path).length <= 100) return { name: path, prefix: '' };
  for (let slash = path.indexOf('/'); slash !== -1; slash = path.indexOf('/', slash + 1)) {
    const prefix = path.slice(0, slash);
    const name = path.slice(slash + 1);
    if (encoder.encode(prefix).length <= 155 && encoder.encode(name).length <= 100) {
      return { name, prefix };
    }
  }
  throw new Error(`The path ${path} is too long to pack; build it with tar instead.`);
}

function entryHeader(entry: TarEntry): Uint8Array {
  const block = new Uint8Array(BLOCK);
  const { name, prefix } = splitPath(entry.path);
  writeField(block, 0, 100, name);
  writeOctal(block, 100, 8, 0o644);
  writeOctal(block, 108, 8, 0);
  writeOctal(block, 116, 8, 0);
  writeOctal(block, 124, 12, entry.data.length);
  writeOctal(block, 136, 12, 0);
  block.fill(32, 148, 156);
  writeField(block, 156, 1, '0');
  writeField(block, 257, 8, 'ustar\u000000');
  writeField(block, 345, 155, prefix);
  const sum = block.reduce((total, byte) => total + byte, 0);
  writeField(block, 148, 8, `${sum.toString(8).padStart(6, '0')}\0 `);
  return block;
}

const padded = (size: number) => Math.ceil(size / BLOCK) * BLOCK;

export function packTar(entries: readonly TarEntry[]): Uint8Array<ArrayBuffer> {
  const total = entries.reduce((sum, entry) => sum + BLOCK + padded(entry.data.length), 2 * BLOCK);
  const archive = new Uint8Array(total);
  let offset = 0;
  for (const entry of entries) {
    archive.set(entryHeader(entry), offset);
    archive.set(entry.data, offset + BLOCK);
    offset += BLOCK + padded(entry.data.length);
  }
  return archive;
}

// A picked folder's files arrive as "dist/index.html"; the folder itself is not part of the site.
export function sitePathsFromFolder(paths: readonly string[]): string[] {
  const tops = new Set(paths.map((path) => (path.includes('/') ? path.split('/')[0] : '')));
  const [top] = [...tops];
  if (tops.size !== 1 || !top) return [...paths];
  return paths.map((path) => path.slice(top.length + 1));
}

// Gzips the archive where the browser can; the server reads plain tar too.
export async function compress(archive: Uint8Array<ArrayBuffer>): Promise<Blob> {
  if (typeof CompressionStream === 'undefined') {
    return new Blob([archive], { type: 'application/x-tar' });
  }
  const source = new ReadableStream<BufferSource>({
    start(controller) {
      controller.enqueue(archive);
      controller.close();
    },
  });
  const gzipped = await new Response(
    source.pipeThrough(new CompressionStream('gzip')),
  ).arrayBuffer();
  return new Blob([gzipped], { type: 'application/gzip' });
}

// Packs files picked with a folder input (each carries webkitRelativePath).
export async function packFolder(files: readonly File[]): Promise<Blob> {
  const paths = sitePathsFromFolder(files.map((file) => file.webkitRelativePath || file.name));
  const entries = await Promise.all(
    files.map(async (file, index) => ({
      path: paths[index],
      data: new Uint8Array(await file.arrayBuffer()),
    })),
  );
  return compress(packTar(entries));
}
