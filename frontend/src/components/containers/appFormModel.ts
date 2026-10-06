import { isValidAppName } from './appName';
import type { App, AppSubmission, EnvVar, SecretRef, VarKind } from '../../api/apps';

export interface EnvRow {
  id: number;
  name: string;
  kind: VarKind;
  value: string;
  variable: string;
  // Typed here, sent once to the write-only endpoint, never read back.
  secretValue: string;
  // Where the server already keeps this variable's value, if it does.
  storedSecret?: SecretRef;
  // The server keys a stored secret by its variable name, so a rename needs the value again.
  storedName?: string;
  replacing: boolean;
}

export interface AppFormValues {
  name: string;
  image: string;
  port: string;
  // An internal service (EXC-525): no HTTP port, no public URL, reached on its internal ports.
  internal: boolean;
  // Comma-separated TCP port numbers (EXC-525); empty means none.
  internalPorts: string;
  replicas: string;
  healthCheckPath: string;
  env: EnvRow[];
  diskEnabled: boolean;
  diskMountPath: string;
  // Whole GiB as typed for a new disk; an attached disk's stored size, such as "500Mi".
  diskSize: string;
  // The container already has a disk: it stays, and its size grows on the container page.
  diskAttached: boolean;
}

export type AppFormErrors = Partial<
  Record<
    | 'name'
    | 'image'
    | 'port'
    | 'internalPorts'
    | 'replicas'
    | 'healthCheckPath'
    | 'diskMountPath'
    | 'diskSize',
    string
  >
> & {
  env?: Record<number, string>;
};

export const DEFAULT_PORT = 8080;
export const DEFAULT_REPLICAS = 1;
export const DEFAULT_DISK_MOUNT = '/data';
export const DEFAULT_DISK_GIB = '1';

export const MAX_SECRET_BYTES = 8 * 1024;

let nextRowId = 0;

export const emptyEnvRow = (): EnvRow => ({
  id: nextRowId++,
  name: '',
  kind: 'literal',
  value: '',
  variable: 'DATABASE_URL',
  secretValue: '',
  replacing: false,
});

const rowFromVar = (v: EnvVar): EnvRow => ({
  ...emptyEnvRow(),
  name: v.name,
  kind: v.kind,
  value: v.value ?? '',
  variable: v.reference?.variable ?? 'DATABASE_URL',
  storedSecret: v.secret,
  storedName: v.secret ? v.name : undefined,
});

export const needsSecretValue = (row: EnvRow) =>
  !row.storedSecret || row.replacing || row.name !== row.storedName;

export const initialValues = (app?: App): AppFormValues =>
  app
    ? {
        name: app.name,
        image: app.image,
        port: app.internal ? '' : String(app.port),
        internal: app.internal === true,
        internalPorts: (app.internalPorts ?? []).map((p) => p.port).join(', '),
        replicas: String(app.replicas),
        healthCheckPath: app.healthCheckPath ?? '',
        env: app.env.map(rowFromVar),
        diskEnabled: app.disk !== undefined,
        diskMountPath: app.disk?.mountPath ?? DEFAULT_DISK_MOUNT,
        diskSize: app.disk?.size ?? DEFAULT_DISK_GIB,
        diskAttached: app.disk !== undefined,
      }
    : {
        name: '',
        image: '',
        port: String(DEFAULT_PORT),
        internal: false,
        internalPorts: '',
        replicas: String(DEFAULT_REPLICAS),
        healthCheckPath: '',
        env: [],
        diskEnabled: false,
        diskMountPath: DEFAULT_DISK_MOUNT,
        diskSize: DEFAULT_DISK_GIB,
        diskAttached: false,
      };

const ENV_NAME = /^[A-Za-z_]\w*$/;
const HEALTH_PATH = /^\/[\w.~/%:@!$&'()*+,;=-]*$/;

function imageError(image: string): string | undefined {
  if (image.trim() === '') return 'Enter the image to run, for example nginx:1.27.';
  if (/\s/.test(image)) return 'The image cannot contain spaces.';
  const lastSlash = image.lastIndexOf('/');
  const hasTag = image.lastIndexOf(':') > lastSlash;
  if (!image.includes('@') && !hasTag) {
    return 'Add a tag or digest to the image, for example nginx:1.27. The latest tag is never assumed.';
  }
  return undefined;
}

const MOUNT_PATH = /^\/[\w.@+-][\w.@+/-]*$/;
// A disk at or under one of these hides the image's own files. /var itself too,
// but /var/lib/<service> is the usual data directory.
const SYSTEM_DIRECTORIES = ['/bin', '/boot', '/etc', '/lib', '/lib32', '/lib64', '/run', '/sbin', '/usr', '/var/run'];
const KERNEL_FILESYSTEMS = ['/proc', '/sys', '/dev'];

const atOrUnderAny = (path: string, roots: string[]) =>
  roots.some((root) => path === root || path.startsWith(`${root}/`));

// Mirrors the server's rules, which still decide.
function mountPathError(path: string): string | undefined {
  const clean = MOUNT_PATH.test(path) && !path.endsWith('/') && !/\/\.{1,2}(\/|$)|\/\//.test(path);
  if (!clean || path.length > 256) {
    return 'Use an absolute path of letters, digits and . _ - @ +, such as /data.';
  }
  if (path === '/var' || atOrUnderAny(path, SYSTEM_DIRECTORIES)) {
    return `${path} is at or under a directory that holds the image's own files; mount the disk at a data directory such as /data.`;
  }
  if (atOrUnderAny(path, KERNEL_FILESYSTEMS)) {
    return `${path} belongs to the container runtime; mount the disk at a data directory such as /data.`;
  }
  return undefined;
}

function diskErrors(values: AppFormValues): AppFormErrors {
  if (!values.diskEnabled) return {};
  const errors: AppFormErrors = {};
  const mount = mountPathError(values.diskMountPath);
  if (mount) errors.diskMountPath = mount;
  if (!values.diskAttached && !wholeNumberIn(values.diskSize, 1, 99999)) {
    errors.diskSize = 'The size must be a whole number of GiB, 1 or more.';
  }
  if (!wholeNumberIn(values.replicas, 0, 1)) {
    errors.replicas = 'A container with a disk runs one copy: choose 0 or 1.';
  }
  return errors;
}

function wholeNumberIn(raw: string, min: number, max: number): boolean {
  if (!/^\d+$/.test(raw)) return false;
  const value = Number(raw);
  return value >= min && value <= max;
}

function secretError(row: EnvRow): string | undefined {
  if (!needsSecretValue(row)) return undefined;
  if (row.secretValue === '') return 'Enter the secret value.';
  if (new TextEncoder().encode(row.secretValue).length > MAX_SECRET_BYTES) {
    return 'The secret value is too long: 8 KB at most.';
  }
  return undefined;
}

function envRowError(row: EnvRow, seen: Set<string>, databaseName?: string): string | undefined {
  if (row.name === '') return 'Give the variable a name.';
  if (!ENV_NAME.test(row.name)) {
    return `${row.name} is not a valid name: use letters, digits and _, not starting with a digit.`;
  }
  if (seen.has(row.name)) return `${row.name} is used twice.`;
  seen.add(row.name);
  if (row.kind === 'reference' && !databaseName)
    return 'This project has no database to connect to yet.';
  if (row.kind === 'secret') return secretError(row);
  return undefined;
}

// The server's set limits (EXC-555): at most 100 variables, and the names and
// plain values within 64 KB counted as it counts them — name, kind, value and
// 64 bytes of framing each. Secrets and references count by number only.
export const MAX_ENV_VARS = 100;
const MAX_TOTAL_ENV_BYTES = 64 * 1024;
const ENV_FRAMING_BYTES = 64;
const utf8Length = (text: string) => new TextEncoder().encode(text).length;

const envRowBytes = (row: EnvRow) =>
  utf8Length(row.name) + row.kind.length + ENV_FRAMING_BYTES + (row.kind === 'literal' ? utf8Length(row.value) : 0);

// Reported on the row that crosses a limit.
function envSetError(index: number, before: number, after: number): string | undefined {
  if (index === MAX_ENV_VARS) return `A container takes at most ${MAX_ENV_VARS} variables.`;
  if (before <= MAX_TOTAL_ENV_BYTES && after > MAX_TOTAL_ENV_BYTES) {
    return 'The variable names and plain values exceed 64 KB in total; secrets and references do not count toward this.';
  }
  return undefined;
}

function envErrors(rows: EnvRow[], databaseName?: string): Record<number, string> {
  const seen = new Set<string>();
  const errors: Record<number, string> = {};
  let total = 0;
  rows.forEach((row, index) => {
    const before = total;
    total += envRowBytes(row);
    const error = envRowError(row, seen, databaseName) ?? envSetError(index, before, total);
    if (error) errors[index] = error;
  });
  return errors;
}

export function validateAppForm(
  values: AppFormValues,
  maxReplicas: number,
  databaseName?: string,
): AppFormErrors {
  const errors: AppFormErrors = {};
  const image = imageError(values.image);
  if (image) errors.image = image;
  if (!isValidAppName(values.name)) {
    errors.name =
      'Use 2 to 50 lowercase letters, digits or hyphens, starting with a letter and ending with a letter or digit. Names starting with proj- are reserved.';
  }
  Object.assign(errors, exposureErrors(values));
  if (!wholeNumberIn(values.replicas, 0, maxReplicas)) {
    errors.replicas = `Choose between 0 and ${maxReplicas} copies.`;
  }
  if (values.healthCheckPath !== '' && !HEALTH_PATH.test(values.healthCheckPath)) {
    errors.healthCheckPath =
      'The health check path must start with / and contain no spaces, ? or #.';
  }
  Object.assign(errors, diskErrors(values));
  const env = envErrors(values.env, databaseName);
  if (Object.keys(env).length > 0) errors.env = env;
  return errors;
}

// The server's rules (EXC-525): at most 8, each 1024-65535, never the HTTP port, no repeats.
export const MAX_INTERNAL_PORTS = 8;

const parseInternalPorts = (raw: string): string[] =>
  raw.trim() === '' ? [] : raw.split(',').map((part) => part.trim());

function internalPortsError(raw: string, httpPort: string): string | undefined {
  const parts = parseInternalPorts(raw);
  if (parts.length > MAX_INTERNAL_PORTS) return `Declare at most ${MAX_INTERNAL_PORTS} internal ports.`;
  if (parts.some((part) => !wholeNumberIn(part, 1024, 65535))) {
    return 'Internal ports are whole numbers between 1024 and 65535, separated by commas.';
  }
  if (parts.includes(httpPort.trim())) return 'An internal port cannot be the HTTP port.';
  if (new Set(parts.map(Number)).size !== parts.length) return 'Each internal port may be listed once.';
  return undefined;
}

// A public web app needs its HTTP port; an internal service needs an internal port instead.
function exposureErrors(values: AppFormValues): AppFormErrors {
  const errors: AppFormErrors = {};
  if (!values.internal && !wholeNumberIn(values.port, 1, 65535)) {
    errors.port = 'Port must be a whole number between 1 and 65535.';
  }
  const internal = internalPortsError(values.internalPorts, values.internal ? '' : values.port);
  if (internal) errors.internalPorts = internal;
  else if (values.internal && parseInternalPorts(values.internalPorts).length === 0) {
    errors.internalPorts = 'An internal service needs at least one internal port, for example 6379.';
  }
  return errors;
}

export const hasErrors = (errors: AppFormErrors) => Object.keys(errors).length > 0;

function toEnvVar(row: EnvRow, databaseName?: string): EnvVar | undefined {
  if (row.kind === 'reference') {
    return {
      name: row.name,
      kind: 'reference',
      reference: { sourceKind: 'database', sourceName: databaseName ?? '', variable: row.variable },
    };
  }
  if (row.kind === 'secret') {
    // A secret the server does not hold yet is added by storing its value.
    return row.storedSecret && row.name === row.storedName
      ? { name: row.name, kind: 'secret', secret: row.storedSecret }
      : undefined;
  }
  return { name: row.name, kind: 'literal', value: row.value };
}

// An attached disk is resized on the container page, so the form sends it back unchanged.
const submittedDiskSize = (values: AppFormValues): string =>
  values.diskAttached ? values.diskSize : `${Number(values.diskSize)}Gi`;

export const toAppSubmission = (values: AppFormValues, databaseName?: string): AppSubmission => ({
  input: {
    name: values.name,
    image: values.image,
    port: values.internal ? 0 : Number(values.port),
    internal: values.internal,
    internalPorts: parseInternalPorts(values.internalPorts).map((part) => ({ port: Number(part), protocol: 'TCP' as const })),
    replicas: Number(values.replicas),
    healthCheckPath: values.internal ? '' : values.healthCheckPath,
    env: values.env.flatMap((row) => toEnvVar(row, databaseName) ?? []),
    ...(values.diskEnabled
      ? { disk: { mountPath: values.diskMountPath, size: submittedDiskSize(values) } }
      : {}),
  },
  secrets: values.env
    .filter((row) => row.kind === 'secret' && needsSecretValue(row))
    .map((row) => ({ name: row.name, value: row.secretValue })),
});
