import type { App, Deploy, DeployStatus } from '../../api/apps';
import type { TierType } from '../../types';

export interface TierDescription {
  label: string;
  detail: string;
  maxReplicas: number;
}

// Mirrors the server's app tier catalog; the server stays the authority and
// refuses anything outside it.
const TIERS: Record<string, TierDescription> = {
  FREE: {
    label: 'Small',
    detail: 'Up to ¼ CPU and 256 MB of memory per copy. 1 copy at most.',
    maxReplicas: 1,
  },
  STANDARD: {
    label: 'Medium',
    detail: 'Up to 1 CPU and 1 GB of memory per copy. Up to 3 copies.',
    maxReplicas: 3,
  },
  ENTERPRISE: {
    label: 'Large',
    detail: 'Up to 2 CPUs and 4 GB of memory per copy. Up to 3 copies.',
    maxReplicas: 3,
  },
};

export const TIER_ORDER: TierType[] = ['FREE', 'STANDARD', 'ENTERPRISE'];

export const describeTier = (tier: TierType): TierDescription =>
  TIERS[tier] ?? { label: tier, detail: 'Size set by the project plan.', maxReplicas: 1 };

export type Tone = 'neutral' | 'progress' | 'success' | 'error' | 'warning';

export const DEPLOY_STATUS: Record<DeployStatus, { label: string; tone: Tone }> = {
  pending: { label: 'Queued', tone: 'progress' },
  rolling: { label: 'Rolling out', tone: 'progress' },
  succeeded: { label: 'Live', tone: 'success' },
  failed: { label: 'Failed', tone: 'error' },
  superseded: { label: 'Replaced by a newer deploy', tone: 'neutral' },
};

export function appDisplayStatus(app: App, lastDeploy?: Deploy): { label: string; tone: Tone } {
  if (app.replicas === 0) return { label: 'Stopped', tone: 'warning' };
  switch (lastDeploy?.status) {
    case undefined:
      return { label: 'Not deployed', tone: 'neutral' };
    case 'succeeded':
      return { label: 'Running', tone: 'success' };
    case 'failed':
      return { label: 'Failed', tone: 'error' };
    default:
      return { label: 'Deploying', tone: 'progress' };
  }
}

// Ordered: the first matching rule wins, so the specific pod reasons come
// before the broader "resolve" rules.
const FAILURE_RULES: Array<[RegExp, string]> = [
  [
    /ImagePullBackOff|ErrImagePull/,
    'The image could not be pulled. Check the image name and tag, and that the registry allows it to be pulled.',
  ],
  [/InvalidImageName/, 'The image name is not valid.'],
  [
    /CrashLoopBackOff/,
    'The app keeps crashing right after it starts. Check that all the variables it needs are set.',
  ],
  [
    /CreateContainerConfigError/,
    'The container could not be set up, usually because a variable points at a secret that does not exist.',
  ],
  [
    /did not become ready within/,
    'The app did not become ready in time. Check that it listens on the port you set and that the health check path answers.',
  ],
  [/Unschedulable/, 'There is no room to run the app right now. Try again later.'],
  [/no namespace to deploy into/, 'The project is not ready to run containers yet.'],
  [
    /no resolver was given/,
    'This installation cannot supply database or secret variables to containers yet.',
  ],
  [/resolve secret/, 'A secret variable could not be read. Check where the secret is stored.'],
  [
    /resolve "/,
    "A database variable could not be resolved. Check that the project's database is running.",
  ],
];

export function plainFailureReason(raw?: string): string {
  if (!raw) return 'The deploy failed.';
  const match = FAILURE_RULES.find(([pattern]) => pattern.test(raw));
  return match ? match[1] : 'The deploy failed.';
}

const APP_NAME = /^[a-z0-9][a-z0-9-]{1,49}$/;

export function suggestAppName(image: string): string {
  const withoutDigest = image.split('@')[0];
  const lastSegment = withoutDigest.split('/').pop() ?? '';
  const repository = lastSegment.split(':')[0];
  const name = repository
    .toLowerCase()
    .replaceAll(/[^a-z0-9]+/g, '-')
    .replaceAll(/^-+|-+$/g, '')
    .slice(0, 50);
  return APP_NAME.test(name) ? name : '';
}

export function formatWhen(iso?: string): string {
  if (!iso) return '';
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? '' : date.toLocaleString();
}
