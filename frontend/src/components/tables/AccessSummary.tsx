interface AccessSummaryProps {
  readonly table: string;
  readonly roles: string[];
}

const SHOWN = 2;

/** The roles that can read a table through the API, at a glance in the table list. */
export function AccessSummary({ table, roles }: AccessSummaryProps) {
  const label =
    roles.length > 0 ? `Readable through the API by ${roles.join(', ')}` : 'No API access: no role can read it';
  return (
    <span
      className="flex-shrink-0 flex items-center gap-0.5 mr-1"
      title={label}
      data-testid={`access-summary-${table}`}
    >
      {roles.length === 0 && <span className="sr-only">No API access</span>}
      {roles.slice(0, SHOWN).map((role) => (
        <span
          key={role}
          className="px-1 py-px text-[10px] leading-tight rounded bg-emerald-500/10 text-emerald-400 border border-emerald-500/30 font-mono"
        >
          {role}
        </span>
      ))}
      {roles.length > SHOWN && (
        <span className="px-1 text-[10px] text-text-tertiary" aria-label={`and ${roles.length - SHOWN} more`}>
          +{roles.length - SHOWN}
        </span>
      )}
    </span>
  );
}
