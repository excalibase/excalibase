export const APPS_CONNECTION_NOTE =
  'Apps you host on Excalibase get DATABASE_URL injected automatically. There is nothing to copy.';

export function AppsConnectionNote() {
  return (
    <span className="text-xs text-text-tertiary" data-testid="apps-database-url-note">
      {APPS_CONNECTION_NOTE}
    </span>
  );
}
