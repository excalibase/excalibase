import { PASSWORD_MAX, PASSWORD_MIN } from '../utils/databasePassword';

interface DatabasePasswordFieldProps {
  readonly id: string;
  readonly label: string;
  readonly value: string;
  readonly onChange: (value: string) => void;
  readonly error?: string;
}

// Blank asks for a generated password, which is the default.
export function DatabasePasswordField({ id, label, value, onChange, error }: DatabasePasswordFieldProps) {
  return (
    <div>
      <label htmlFor={id} className="block text-sm font-medium text-text-primary mb-1.5">{label}</label>
      <input
        id={id} type="password" autoComplete="new-password" value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder="Generate for me"
        aria-invalid={error ? true : undefined}
        aria-describedby={`${id}-help`}
        className="w-full px-4 py-2.5 bg-bg-tertiary border border-border-primary rounded-lg text-text-primary placeholder:text-text-tertiary focus:outline-none focus:ring-2 focus:ring-accent-primary"
      />
      {error ? (
        <p id={`${id}-help`} data-testid="password-error" role="alert" className="text-xs text-color-error mt-1">{error}</p>
      ) : (
        <p id={`${id}-help`} className="text-xs text-text-tertiary mt-1">
          {PASSWORD_MIN} to {PASSWORD_MAX} characters, or a passphrase of several words. Leave it blank and we generate a strong one for you.
          It is shown once, so save it when it appears.
        </p>
      )}
    </div>
  );
}
