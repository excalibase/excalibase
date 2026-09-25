import { EJSON } from 'bson';
import { parseFilter, parseProject, parseSort } from 'mongodb-query-parser';
import { QUERY_OPERATORS } from '@mongodb-js/mongodb-constants';
import type { Completion, CompletionContext, CompletionSource } from '@codemirror/autocomplete';

export type QueryPart = 'filter' | 'sort' | 'projection';

export type QueryText = Record<QueryPart, string>;

export type ServerQuery = Partial<Record<QueryPart, string>>;

export type ServerQueryResult =
  | { ok: true; value: ServerQuery }
  | { ok: false; part: QueryPart; error: string };

const parsers: Record<QueryPart, (text: string) => unknown> = {
  filter: parseFilter,
  sort: parseSort,
  projection: parseProject,
};

function isDocument(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

// Shell syntax ({a: {$gt: 5}}, ObjectId("...")) becomes canonical Extended
// JSON, which the server reads without losing a type.
export function toServerQuery(text: QueryText): ServerQueryResult {
  const value: ServerQuery = {};
  for (const part of Object.keys(parsers) as QueryPart[]) {
    const source = text[part].trim();
    if (source === '') continue;
    let parsed: unknown;
    try {
      parsed = parsers[part](source);
    } catch (err) {
      return { ok: false, part, error: `The ${part} does not parse: ${(err as Error).message.split('\n')[0]}` };
    }
    if (parsed === null || parsed === undefined) continue;
    if (!isDocument(parsed)) {
      return { ok: false, part, error: `The ${part} must be a document, like { field: value }` };
    }
    if (Object.keys(parsed).length > 0) {
      value[part] = EJSON.stringify(parsed, { relaxed: false });
    }
  }
  return { ok: true, value };
}

const maxDepth = 8;

function isExtendedJSONValue(value: Record<string, unknown>): boolean {
  const keys = Object.keys(value);
  return keys.length > 0 && keys.every((key) => key.startsWith('$'));
}

function collectPaths(value: unknown, prefix: string, depth: number, into: Set<string>) {
  if (depth > maxDepth) return;
  if (Array.isArray(value)) {
    for (const item of value) collectPaths(item, prefix, depth, into);
    return;
  }
  if (!isDocument(value) || isExtendedJSONValue(value)) return;
  for (const [key, child] of Object.entries(value)) {
    const path = prefix ? `${prefix}.${key}` : key;
    into.add(path);
    collectPaths(child, path, depth + 1, into);
  }
}

// Every dotted field path in the sampled documents, for the query bar's
// suggestions. Arrays of documents contribute their elements' fields.
export function fieldPaths(documents: readonly unknown[]): string[] {
  const paths = new Set<string>();
  for (const doc of documents) {
    if (isDocument(doc)) collectPaths(doc, '', 0, paths);
  }
  return [...paths].sort((a, b) => a.localeCompare(b));
}

const operatorCompletions: Completion[] = QUERY_OPERATORS.map((op) => ({
  label: op.value,
  type: 'keyword',
  detail: op.meta,
}));

function fieldCompletion(path: string): Completion {
  return path.includes('.') ? { label: path, type: 'property', apply: `"${path}"` } : { label: path, type: 'property' };
}

export function mongoCompletionSource(fields: () => readonly string[], withOperators: boolean): CompletionSource {
  return (context: CompletionContext) => {
    const word = context.matchBefore(/[\w.$]*/);
    if (!word || (word.from === word.to && !context.explicit)) return null;
    if (word.text.startsWith('$')) {
      return withOperators ? { from: word.from, options: operatorCompletions, validFor: /^\$\w*$/ } : null;
    }
    return { from: word.from, options: fields().map(fieldCompletion), validFor: /^[\w.]*$/ };
  };
}

export type DocumentTextResult = { ok: true } | { ok: false; error: string };

// A document typed into the editor must be a JSON object; Extended JSON
// wrappers such as {"$oid": "..."} are read by the server.
export function parseDocumentText(text: string): DocumentTextResult {
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (err) {
    return { ok: false, error: `Not valid JSON: ${(err as Error).message}` };
  }
  return isDocument(parsed) ? { ok: true } : { ok: false, error: 'A document must be a JSON object' };
}

export function documentIdParam(doc: Record<string, unknown>): string | null {
  return '_id' in doc ? JSON.stringify(doc['_id']) : null;
}
