export const GENERIC_PROVISION_FAILURE = 'Provisioning failed. Try again or contact support.';

// Ordered: the first matching rule wins, so the specific causes come before the broad ones.
// What the server records can name Kubernetes objects; only these sentences reach a customer.
const RULES: ReadonlyArray<readonly [RegExp, string]> = [
  [/OOMKilled|out of memory/i, 'It ran out of memory while starting. Try again, or move the project to a plan with more memory.'],
  [
    /persistentvolumeclaim|\bPVC\b|FailedAttachVolume|FailedMount|Multi-Attach/i,
    'Its storage could not be attached. Try again in a few minutes.',
  ],
  [/ImagePullBackOff|ErrImagePull|InvalidImageName/i, 'The image could not be downloaded. Try again in a few minutes.'],
  [/CrashLoopBackOff/i, 'It kept crashing while starting. Try again, or contact support if it keeps happening.'],
  [/Unschedulable|Insufficient (?:cpu|memory)|nodes? are available/i, 'There is no room to run it right now. Try again later.'],
  [/timed out|timeout|deadline exceeded|did not become ready/i, 'It took too long to start. Try again in a few minutes.'],
];

export const PROVISION_FAILURE_MESSAGES: readonly string[] = RULES.map(([, message]) => message);

// The customer wording for a provisioning failure; unknown text is never passed through.
export function plainProvisionFailure(raw: string | undefined): string {
  const text = raw?.trim();
  if (!text) return GENERIC_PROVISION_FAILURE;
  return RULES.find(([pattern]) => pattern.test(text))?.[1] ?? GENERIC_PROVISION_FAILURE;
}
