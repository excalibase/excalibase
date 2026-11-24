const WHAT_TO_DO = 'Delete it and add it again. If it keeps failing, tell us.';

export const GENERIC_PROVISION_FAILURE = `Provisioning failed. ${WHAT_TO_DO}`;

// Ordered: the first matching rule wins, so the specific causes come before the broad ones.
// What the server records can name Kubernetes objects; only these sentences reach a customer.
const RULES: ReadonlyArray<readonly [RegExp, string]> = [
  [/OOMKilled|out of memory/i, `It ran out of memory while starting. ${WHAT_TO_DO}`],
  [
    /persistentvolumeclaim|\bPVC\b|FailedAttachVolume|FailedMount|Multi-Attach/i,
    `Its storage could not be attached. ${WHAT_TO_DO}`,
  ],
  [/ImagePullBackOff|ErrImagePull|InvalidImageName/i, `The image could not be downloaded. ${WHAT_TO_DO}`],
  [/CrashLoopBackOff/i, `It kept crashing while starting. ${WHAT_TO_DO}`],
  [/Unschedulable|Insufficient (?:cpu|memory)|nodes? are available/i, `There is no room to run it right now. ${WHAT_TO_DO}`],
  // Anchored to start-up wording, so a database's own "lock timeout" or an etcd timeout stays generic.
  [
    /did not become ready|timed out waiting for|(?:provision\w*|readiness)\W.*?(?:timed out|timeout|deadline exceeded)/i,
    `It took too long to start. ${WHAT_TO_DO}`,
  ],
];

export const PROVISION_FAILURE_MESSAGES: readonly string[] = RULES.map(([, message]) => message);

// The customer wording for a provisioning failure; unknown text is never passed through.
export function plainProvisionFailure(raw: string | undefined): string {
  const text = raw?.trim();
  if (!text) return GENERIC_PROVISION_FAILURE;
  return RULES.find(([pattern]) => pattern.test(text))?.[1] ?? GENERIC_PROVISION_FAILURE;
}
