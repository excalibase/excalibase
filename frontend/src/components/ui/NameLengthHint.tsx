import { identifierBytes, identifierError, MAX_IDENTIFIER_BYTES, newNameError } from '../../utils/names';

// A live count under a name field, and the refusal once Postgres would cut it
// short. With newName it also holds a table/column name to the API's rule.
export function NameLengthHint({
  kind,
  name,
  testId,
  newName = false,
}: {
  readonly kind: string;
  readonly name: string;
  readonly testId: string;
  readonly newName?: boolean;
}) {
  const counted = newName ? name.trim() : name;
  const error = newName ? newNameError(kind, name) : identifierError(kind, name);
  return (
    <div className="mt-1 flex items-start justify-between gap-2 text-xs">
      {error ? (
        <p role="alert" className="text-red-400">
          {error}
        </p>
      ) : (
        <span className="text-text-tertiary">Up to {MAX_IDENTIFIER_BYTES} characters</span>
      )}
      <span className={error ? 'text-red-400' : 'text-text-tertiary'} data-testid={testId}>
        {identifierBytes(counted)}/{MAX_IDENTIFIER_BYTES}
      </span>
    </div>
  );
}
