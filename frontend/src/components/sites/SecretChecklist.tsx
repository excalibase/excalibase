import { apiErrorMessage } from '../../api/apps';

export interface SecretRow {
  readonly name: string;
  readonly checked: boolean;
}

// One checkbox per project secret, with the save error under the list; the callers say what a tick means.
export function SecretChecklist({
  rows,
  pending,
  error,
  testIdPrefix,
  onToggle,
}: {
  readonly rows: readonly SecretRow[];
  readonly pending: boolean;
  readonly error: unknown;
  readonly testIdPrefix: string;
  readonly onToggle: (name: string, on: boolean) => void;
}) {
  return (
    <>
      <ul className="space-y-1">
        {rows.map((row) => (
          <li key={row.name}>
            <label className="flex items-center gap-2 text-sm text-text-primary">
              <input
                type="checkbox"
                checked={row.checked}
                disabled={pending}
                onChange={(event) => onToggle(row.name, event.target.checked)}
                data-testid={`${testIdPrefix}-${row.name}`}
              />
              <code>{row.name}</code>
            </label>
          </li>
        ))}
      </ul>
      {error && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(error, 'The change could not be saved')}
        </p>
      )}
    </>
  );
}
