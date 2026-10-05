import { useState } from 'react';
import { Eye, EyeOff } from 'lucide-react';

// Mirrors the server's isValidPassword so a password it would refuse never leaves the form.
export function passwordProblem(value: string): string | undefined {
  if (value.length < 8) return 'Min 8 characters';
  const hasMixedCase = /[A-Z]/.test(value) && /[a-z]/.test(value);
  if (!hasMixedCase || !/\d/.test(value)) return 'Mixed case + at least one digit';
  return undefined;
}

export function newPasswordReady(password: string, confirm: string): boolean {
  return passwordProblem(password) === undefined && password === confirm;
}

const PASSWORD_HINT = 'Min 8 chars, mixed case, at least one digit';

interface NewPasswordFieldsProps {
  readonly id: string;
  readonly label: string;
  readonly password: string;
  readonly confirm: string;
  readonly onPasswordChange: (value: string) => void;
  readonly onConfirmChange: (value: string) => void;
  readonly disabled?: boolean;
  readonly placeholder?: string;
  readonly testId?: string;
  readonly inputClassName?: string;
}

const DEFAULT_INPUT_CLASS =
  'w-full px-3 py-2.5 pr-10 bg-bg-secondary border border-border-primary rounded-lg text-text-primary placeholder:text-text-tertiary focus:outline-none focus:ring-2 focus:ring-purple-500 focus:border-transparent transition-colors';

export function NewPasswordFields({
  id, label, password, confirm, onPasswordChange, onConfirmChange,
  disabled = false, placeholder, testId, inputClassName = DEFAULT_INPUT_CLASS,
}: NewPasswordFieldsProps) {
  const [visible, setVisible] = useState(false);
  const confirmId = `${id}-confirm`;
  const ruleId = `${id}-rule`;
  const mismatchId = `${confirmId}-mismatch`;
  const problem = password ? passwordProblem(password) : undefined;
  const mismatch = confirm !== '' && confirm !== password;
  const type = visible ? 'text' : 'password';

  return (
    <div className="space-y-4">
      <div>
        <label htmlFor={id} className="block text-sm font-medium text-text-secondary mb-1.5">{label}</label>
        <div className="relative">
          <input id={id} type={type} autoComplete="new-password" value={password}
            onChange={(e) => onPasswordChange(e.target.value)} disabled={disabled} placeholder={placeholder}
            aria-invalid={problem !== undefined} aria-describedby={ruleId}
            className={inputClassName} data-testid={testId} required />
          <button type="button" onClick={() => setVisible((v) => !v)} aria-label="Show passwords" aria-pressed={visible}
            className="absolute inset-y-0 right-0 flex items-center px-3 text-text-tertiary hover:text-text-primary">
            {visible ? <EyeOff className="w-4 h-4" aria-hidden="true" /> : <Eye className="w-4 h-4" aria-hidden="true" />}
          </button>
        </div>
        <p id={ruleId} className={`text-xs mt-1 ${problem ? 'text-red-400' : 'text-text-tertiary'}`}>
          {problem ?? PASSWORD_HINT}
        </p>
      </div>
      <div>
        <label htmlFor={confirmId} className="block text-sm font-medium text-text-secondary mb-1.5">
          Confirm {label.toLowerCase()}
        </label>
        <input id={confirmId} type={type} autoComplete="new-password" value={confirm}
          onChange={(e) => onConfirmChange(e.target.value)} disabled={disabled}
          aria-invalid={mismatch} aria-describedby={mismatch ? mismatchId : undefined}
          className={inputClassName} data-testid={testId && `${testId}-confirm`} required />
        <p id={mismatchId} aria-live="polite" className="text-xs mt-1 text-red-400">
          {mismatch ? "Passwords don't match" : ''}
        </p>
      </div>
    </div>
  );
}
