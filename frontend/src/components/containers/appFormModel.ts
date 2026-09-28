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
  replicas: string;
  healthCheckPath: string;
  env: EnvRow[];
  diskEnabled: boolean;
  diskMountPath: string;
  // Whole GiB, as typed.
  diskSize: string;
  // The container already has a disk: it stays, and its size grows on the container page.
  diskAttached: boolean;
}

export type AppFormErrors = Partial<
  Record<
    'name' | 'image' | 'port' | 'replicas' | 'healthCheckPath' | 'diskMountPath' | 'diskSize',
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
        port: String(app.port),
        replicas: String(app.replicas),
        healthCheckPath: app.healthCheckPath ?? '',
        env: app.env.map(rowFromVar),
        diskEnabled: app.disk !== undefined,
        diskMountPath: app.disk?.mountPath ?? DEFAULT_DISK_MOUNT,
        diskSize: app.disk ? app.disk.size.replace(/Gi$/, '') : DEFAULT_DISK_GIB,
        diskAttached: app.disk !== undefined,
      }
    : {
        name: '',
        image: '',
        port: String(DEFAULT_PORT),
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
const SYSTEM_DIRECTORIES = new Set([
  '/bin',
  '/boot',
  '/etc',
  '/lib',
  '/lib32',
  '/lib64',
  '/run',
  '/sbin',
  '/usr',
  '/var',
  '/var/run',
]);
const KERNEL_FILESYSTEMS = ['/proc', '/sys', '/dev'];

// Mirrors the server's rules, which still decide.
function mountPathError(path: string): string | undefined {
  const clean = MOUNT_PATH.test(path) && !path.endsWith('/') && !/\/\.{1,2}(\/|$)|\/\//.test(path);
  if (!clean || path.length > 256) {
    return 'Use an absolute path of letters, digits and . _ - @ +, such as /data.';
  }
  if (SYSTEM_DIRECTORIES.has(path)) {
    return `${path} holds the image's own files; mount the disk at a data directory such as /data.`;
  }
  if (KERNEL_FILESYSTEMS.some((root) => path === root || path.startsWith(`${root}/`))) {
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

function envErrors(rows: EnvRow[], databaseName?: string): Record<number, string> {
  const seen = new Set<string>();
  const errors: Record<number, string> = {};
  rows.forEach((row, index) => {
    const error = envRowError(row, seen, databaseName);
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
  if (!wholeNumberIn(values.port, 1, 65535))
    errors.port = 'Port must be a whole number between 1 and 65535.';
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

export const toAppSubmission = (values: AppFormValues, databaseName?: string): AppSubmission => ({
  input: {
    name: values.name,
    image: values.image,
    port: Number(values.port),
    replicas: Number(values.replicas),
    healthCheckPath: values.healthCheckPath,
    env: values.env.flatMap((row) => toEnvVar(row, databaseName) ?? []),
    ...(values.diskEnabled
      ? { disk: { mountPath: values.diskMountPath, size: `${Number(values.diskSize)}Gi` } }
      : {}),
  },
  secrets: values.env
    .filter((row) => row.kind === 'secret' && needsSecretValue(row))
    .map((row) => ({ name: row.name, value: row.secretValue })),
});
