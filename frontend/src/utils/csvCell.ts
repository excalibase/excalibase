// csvCell writes one CSV cell for Studio's export. A cell a spreadsheet would
// evaluate (=, +, -, @, tab, CR) gets a leading apostrophe, so data imported
// as text cannot become a formula when the export is opened (OWASP CSV
// injection). Plain numbers are left as they are.
const FORMULA_START = /^[=+\-@\t\r]/;
const NUMBER = /^-?\d+(\.\d+)?$/;

function stringify(value: unknown): string {
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean' || typeof value === 'bigint')
    return String(value);
  try {
    return JSON.stringify(value);
  } catch {
    return '';
  }
}

export function csvCell(value: unknown): string {
  if (value === null || value === undefined) return '';
  let text = stringify(value);
  if (FORMULA_START.test(text) && !NUMBER.test(text)) text = `'${text}`;
  return `"${text.replaceAll('"', '""')}"`;
}
