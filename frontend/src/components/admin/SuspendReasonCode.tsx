// The rule a suspension is for. It picks the email the account holder gets
// (EXC-841), so the list matches the server's reason codes.
export const SUSPEND_REASON_CODES = [
  { value: 'proxy_vpn', label: 'Proxy or VPN service' },
  { value: 'crypto_mining', label: 'Cryptocurrency mining' },
  { value: 'spam_or_phishing', label: 'Spam or phishing' },
  { value: 'disposable_email', label: 'Temporary email address' },
  { value: 'other', label: 'Other' },
] as const;

export type SuspendReasonCodeValue = (typeof SUSPEND_REASON_CODES)[number]['value'];

interface Props {
  readonly value: SuspendReasonCodeValue;
  readonly onChange: (value: SuspendReasonCodeValue) => void;
}

export function SuspendReasonCode({ value, onChange }: Props) {
  return (
    <label className="block text-sm text-text-secondary">
      Rule broken
      <select
        className="mt-1 w-full rounded-lg bg-bg-secondary border border-border-primary p-2 text-text-primary"
        value={value}
        onChange={(event) => onChange(event.target.value as SuspendReasonCodeValue)}
      >
        {SUSPEND_REASON_CODES.map((code) => (
          <option key={code.value} value={code.value}>{code.label}</option>
        ))}
      </select>
      <span className="block text-xs text-text-tertiary mt-1">Picks the email the account holder receives, when suspension emails are on.</span>
    </label>
  );
}
