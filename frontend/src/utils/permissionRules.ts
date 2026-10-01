// Client-side mirror of the control plane's permission rules
// (server-go/internal/permissions, docs/features/permissions.md §1, §4). The
// server stays the judge; this only catches a mistake before it is sent.

export const MAX_EXPRESSION_DEPTH = 16;
export const MAX_EXPRESSION_NODES = 200;

export const ROLE_PATTERN = /^[a-z][a-z0-9_]{0,62}$/;
const COLUMN_NAME = /^[A-Za-z_][A-Za-z0-9_$]{0,62}$/;
const QUALIFIED_PART = /^[a-z_][a-z0-9_$]{0,62}$/;
const SESSION_VARIABLE = /^x-excalibase-[a-z0-9_-]+$/i;
const SESSION_PREFIX = 'x-excalibase-';

export const USER_ID_VARIABLE = 'X-Excalibase-User-Id';

const COMPARISON_OPERATORS = new Set([
  '_eq', '_neq', '_gt', '_gte', '_lt', '_lte', '_in', '_nin', '_like', '_nlike', '_is_null',
]);

export type BoolExp = Record<string, unknown>;

export function isSessionVariable(value: string): boolean {
  return SESSION_VARIABLE.test(value);
}

function looksLikeSessionVariable(value: string): boolean {
  return value.toLowerCase().startsWith(SESSION_PREFIX);
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

export function isQualifiedName(name: string): boolean {
  const parts = name.split('.');
  return parts.length === 2 && parts.every((part) => QUALIFIED_PART.test(part));
}

export function isColumnName(name: string): boolean {
  return COLUMN_NAME.test(name);
}

// Walks one expression the way the server's expWalker does, counting nodes
// and depth. Throws the first problem as an Error.
class ExpressionWalker {
  private nodes = 0;

  private count(depth: number) {
    this.nodes += 1;
    if (this.nodes > MAX_EXPRESSION_NODES) {
      throw new Error(`expression has more than ${MAX_EXPRESSION_NODES} nodes`);
    }
    if (depth > MAX_EXPRESSION_DEPTH) {
      throw new Error(`expression is nested deeper than ${MAX_EXPRESSION_DEPTH}`);
    }
  }

  exp(value: unknown, depth: number) {
    this.count(depth);
    if (!isPlainObject(value)) throw new Error('an expression must be a JSON object');
    for (const [key, inner] of Object.entries(value)) this.term(key, inner, depth);
  }

  private term(key: string, value: unknown, depth: number) {
    if (key === '_and' || key === '_or') return this.list(key, value, depth);
    if (key === '_not') return this.exp(value, depth + 1);
    if (key === '_exists') return this.exists(value, depth);
    if (!COLUMN_NAME.test(key)) throw new Error(`"${key}" is not a column or relationship name`);
    if (!isPlainObject(value)) throw new Error(`"${key}" must map to an object`);
    if (Object.keys(value).some((op) => COMPARISON_OPERATORS.has(op))) {
      return this.comparison(key, value, depth);
    }
    // No comparison operator: a relationship to the related table's rows.
    return this.exp(value, depth + 1);
  }

  private list(key: string, value: unknown, depth: number) {
    if (!Array.isArray(value)) throw new Error(`${key} takes an array of expressions`);
    for (const item of value) this.exp(item, depth + 1);
  }

  private exists(value: unknown, depth: number) {
    const shaped = isPlainObject(value) && Object.keys(value).length === 2 && '_table' in value && '_where' in value;
    if (!shaped) throw new Error('_exists takes exactly {"_table": "schema.table", "_where": expression}');
    const { _table: table, _where: where } = value as { _table: unknown; _where: unknown };
    if (typeof table !== 'string') throw new Error('_exists._table must be a string');
    if (!isQualifiedName(table)) {
      throw new Error(`_exists._table: "${table}" is not schema.name in lower-case identifiers`);
    }
    this.exp(where, depth + 1);
  }

  private comparison(column: string, ops: Record<string, unknown>, depth: number) {
    this.count(depth + 1);
    for (const [op, operand] of Object.entries(ops)) {
      if (!COMPARISON_OPERATORS.has(op)) throw new Error(`"${column}": "${op}" is not a comparison operator`);
      const problem = operandProblem(op, operand);
      if (problem) throw new Error(`"${column}" ${op}: ${problem}`);
    }
  }
}

function stringOperandProblem(text: string): string | null {
  if (looksLikeSessionVariable(text) && !isSessionVariable(text)) {
    return `"${text}" is not a valid session variable`;
  }
  return null;
}

function listOperandProblem(value: unknown): string | null {
  const refusal = 'takes an array or a session variable holding one';
  if (typeof value === 'string') return isSessionVariable(value) ? null : refusal;
  if (!Array.isArray(value)) return refusal;
  for (const item of value) {
    if (typeof item === 'string') {
      if (looksLikeSessionVariable(item)) return 'an array element cannot be a session variable';
    } else if (typeof item !== 'number' && typeof item !== 'boolean') {
      return 'array elements must be strings, numbers or booleans';
    }
  }
  return null;
}

function operandProblem(op: string, value: unknown): string | null {
  if (op === '_is_null') return typeof value === 'boolean' ? null : 'takes true or false';
  if (op === '_in' || op === '_nin') return listOperandProblem(value);
  if (op === '_like' || op === '_nlike') {
    return typeof value === 'string' ? stringOperandProblem(value) : 'takes a string pattern or a session variable';
  }
  if (value === null) return 'null is not comparable; use _is_null';
  if (typeof value === 'string') return stringOperandProblem(value);
  if (typeof value === 'number' || typeof value === 'boolean') return null;
  return 'takes a string, number, boolean or session variable';
}

/** Returns null when the expression follows the grammar, else why not. */
export function validateBoolExp(exp: unknown): string | null {
  try {
    new ExpressionWalker().exp(exp, 1);
    return null;
  } catch (err) {
    return (err as Error).message;
  }
}

/** Parses editor text and validates it. */
export function validateBoolExpText(text: string): { value: BoolExp | null; error: string | null } {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return { value: null, error: 'not valid JSON' };
  }
  const error = validateBoolExp(parsed);
  return error ? { value: null, error } : { value: parsed as BoolExp, error: null };
}

export function ownerOnlyExpression(column: string): BoolExp {
  return { [column]: { _eq: USER_ID_VARIABLE } };
}

const ROLE_SHAPE = 'must be lower-case letters, digits and underscores, starting with a letter, at most 63 characters';

/** A role a table or function permission can name. */
export function validatePermissionRole(role: string): string | null {
  if (role === 'service') return 'service bypasses permissions, so a permission for it means nothing';
  if (!ROLE_PATTERN.test(role)) return `role ${ROLE_SHAPE}`;
  return null;
}

/** A role an end user may be given: never one of the reserved names. */
export function validateEndUserRole(role: string): string | null {
  if (role === 'anon' || role === 'service' || role.startsWith('pg_') || role.startsWith('excalibase_')) {
    return `"${role}" is reserved and cannot be given to an end user`;
  }
  if (!ROLE_PATTERN.test(role)) return `role ${ROLE_SHAPE}`;
  return null;
}

// Splits an argument list on top-level commas (a type may carry parentheses).
function splitArguments(list: string): string[] {
  const parts: string[] = [];
  let depth = 0;
  let current = '';
  for (const char of list) {
    if (char === '(') depth += 1;
    if (char === ')') depth -= 1;
    if (char === ',' && depth === 0) {
      parts.push(current);
      current = '';
    } else {
      current += char;
    }
  }
  parts.push(current);
  return parts.map((part) => part.trim()).filter(Boolean);
}

const ARGUMENT_MODES = new Set(['in', 'out', 'inout', 'variadic']);

/**
 * The names of the json/jsonb arguments in a Postgres identity argument list
 * ("p_query text, session jsonb"): the only ones a session argument may be.
 */
export function jsonArguments(identityArguments: string): string[] {
  const names: string[] = [];
  for (const argument of splitArguments(identityArguments)) {
    const words = argument.split(/\s+/);
    if (words.length > 2 && ARGUMENT_MODES.has(words[0].toLowerCase())) words.shift();
    if (words.length !== 2) continue;
    const [name, type] = words;
    if (type === 'json' || type === 'jsonb') names.push(name);
  }
  return names;
}
