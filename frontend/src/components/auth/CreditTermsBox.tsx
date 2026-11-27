import { useCreditTerms } from '../../hooks/useDeploymentMode';

interface CreditTermsBoxProps {
  readonly accepted: boolean;
  readonly onChange: (accepted: boolean) => void;
  readonly disabled?: boolean;
}

// CreditTermsBox is the sign-up credit's terms checkbox. It shows only while
// credits are on, and leaving it empty still creates the account, without the credit.
export function CreditTermsBox({ accepted, onChange, disabled }: CreditTermsBoxProps) {
  const terms = useCreditTerms();
  if (!terms) return null;
  return (
    <label className="flex items-start gap-2 text-sm text-text-secondary">
      <input type="checkbox" checked={accepted} disabled={disabled} onChange={(e) => onChange(e.target.checked)} className="mt-1" />
      <span>
        I accept the{' '}
        {terms.termsUrl ? (
          <a href={terms.termsUrl} target="_blank" rel="noreferrer" className="text-purple-400 hover:text-purple-300">credit terms</a>
        ) : 'credit terms'}
        {' '}and want the sign-up credit.
      </span>
    </label>
  );
}
