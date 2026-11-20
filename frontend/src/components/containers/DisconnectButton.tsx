import { useState } from 'react';
import { secondaryButton } from './ContainerBits';

// Disconnecting cancels the builds in flight, so it asks first, in the page.
export function DisconnectButton({
  testId,
  pending,
  onConfirm,
}: {
  readonly testId: string;
  readonly pending: boolean;
  readonly onConfirm: () => void;
}) {
  const [asking, setAsking] = useState(false);
  if (!asking) {
    return (
      <button
        type="button"
        className={secondaryButton}
        disabled={pending}
        onClick={() => setAsking(true)}
        data-testid={testId}
      >
        Disconnect
      </button>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-2" data-testid={`${testId}-warning`}>
      <span className="text-xs text-amber-400">
        Disconnecting cancels any queued or running builds and stops deploys on push.
      </span>
      <button
        type="button"
        className={secondaryButton}
        disabled={pending}
        onClick={() => {
          onConfirm();
          setAsking(false);
        }}
        data-testid={`${testId}-confirm`}
      >
        Disconnect
      </button>
      <button
        type="button"
        className={secondaryButton}
        onClick={() => setAsking(false)}
        data-testid={`${testId}-cancel`}
      >
        Keep it
      </button>
    </div>
  );
}
