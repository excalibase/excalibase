interface NotOfferedProps {
  readonly appHosting: boolean;
  readonly storage: boolean;
}

// Said where the install leaves a service out: a plain note, not an error.
export function NotOffered({ appHosting, storage }: NotOfferedProps) {
  const missing = [...(appHosting ? [] : ['App hosting']), ...(storage ? [] : ['file storage'])];
  if (missing.length === 0) return null;
  const text = missing.length === 2 ? `${missing[0]} and ${missing[1]} are` : `${missing[0].charAt(0).toUpperCase()}${missing[0].slice(1)} is`;
  return (
    <p className="text-xs text-text-tertiary" data-testid="not-offered">
      {text} not offered on this install.
    </p>
  );
}
