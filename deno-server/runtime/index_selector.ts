// Phase 6.5 — auto-index selection for `ctx.db.query(...)`.
//
// Convex picks an index by matching the filter's leading equality
// predicates against declared indexes. Phase 6 keeps `.withIndex()`
// explicit; this module adds the auto-selection step so handlers
// ported from Convex don't need to add hints by hand.
//
// Contract:
//   * Pure function. No DB. No side effects. Importable from both the
//     worker shim (where the schema lives) and unit tests.
//   * Caller MUST skip auto-selection when `plan.index` is already set
//     — explicit `.withIndex(name, ...)` is always final.
//   * Composite indexes match on the LEADING prefix only. `[a, b, c]`
//     with filter eq(a) returns one bound; with eq(a) AND eq(b) returns
//     two; with eq(b) alone returns none (b is not the leading column).
//   * Tie-break by index name lex order so two equally-applicable
//     indexes deterministically pick the same one.
//
// What's NOT covered (deferred to a follow-up):
//   * Range bounds (gt/gte/lt/lte) in the filter — currently only `eq`
//     contributes to prefix matching. A composite `[a, b]` with filter
//     `eq(a) AND gt(b)` matches only on `a`; `b` becomes a post-filter.
//     Convex documents the same behaviour: range column must be the
//     trailing field, and we'd need to thread the bound through the
//     plan compiler to support it cleanly.
//   * `withSearchIndex` / `withVectorIndex` selection — search and vector
//     surfaces still require explicit hints in Phase 6.5. Convex auto-
//     selects search indexes too, but the heuristic is different (the
//     `.search()` call already names the field, not the index, so the
//     map there is field → indexName, not filter → indexName).

import type { FilterExpr, IndexBound, IndexHint, QueryPlan } from "./db.ts";
import type { IndexDef } from "./schema.ts";

/**
 * Walk an `and`-chained filter tree and collect every top-level `eq`
 * predicate into a `field → value` map. Nested `or`/`not` short-circuit
 * the recursion — an OR'd filter is not a prefix-match candidate.
 *
 * Returns null when the filter is a pure OR/NOT (no usable leading eq).
 */
function collectTopLevelEqs(
  expr: FilterExpr | undefined,
): Map<string, unknown> | null {
  if (expr === undefined) return new Map();
  const out = new Map<string, unknown>();
  function walk(node: FilterExpr): boolean {
    switch (node.kind) {
      case "eq": {
        if (node.left.kind === "field") {
          out.set(node.left.name, node.right);
        }
        return true;
      }
      case "and": {
        for (const a of node.args) {
          if (!walk(a)) return false;
        }
        return true;
      }
      case "or":
        // Disjunction breaks the prefix-match contract — any branch could
        // route a different row to a different index. Bail out cleanly.
        return false;
      case "not":
        // Negation flips selectivity; we treat it as "no usable eq".
        return false;
      // Range and other comparison kinds don't contribute to the eq-prefix
      // set, but they don't poison it either — the caller still gets to
      // use whatever eqs are present.
      case "neq":
      case "gt":
      case "gte":
      case "lt":
      case "lte":
      case "field":
        return true;
      default:
        return true;
    }
  }
  const ok = walk(expr);
  return ok ? out : null;
}

/**
 * Count how many leading fields of `index.fields` are present in `eqs`.
 * Stops at the first missing field — prefix is contiguous.
 */
function leadingPrefixLength(
  fields: readonly string[],
  eqs: ReadonlyMap<string, unknown>,
): number {
  let i = 0;
  for (; i < fields.length; i++) {
    if (!eqs.has(fields[i])) break;
  }
  return i;
}

/**
 * Pure selector — returns the best-matching index for `plan.filter` over
 * `indexes`, or null when no index covers any leading eq.
 *
 * Algorithm: greedy longest-prefix match. We compute the eq-prefix length
 * for every index and pick the largest. Ties resolve by index name lex
 * order. Cost is O(I * F) where I = number of indexes, F = average field
 * count — both tiny in practice.
 */
export function selectIndex(
  plan: QueryPlan,
  indexes: readonly IndexDef[],
): IndexHint | null {
  if (!Array.isArray(indexes) || indexes.length === 0) return null;
  const eqs = collectTopLevelEqs(plan.filter);
  if (eqs === null || eqs.size === 0) return null;

  // Score every index by prefix length; keep only those with prefix > 0.
  type Scored = { def: IndexDef; prefix: number };
  const scored: Scored[] = [];
  for (const def of indexes) {
    if (!Array.isArray(def.fields) || def.fields.length === 0) continue;
    const p = leadingPrefixLength(def.fields, eqs);
    if (p > 0) scored.push({ def, prefix: p });
  }
  if (scored.length === 0) return null;

  // Longest prefix wins; lex on name breaks ties.
  scored.sort((a, b) => {
    if (b.prefix !== a.prefix) return b.prefix - a.prefix;
    return a.def.name < b.def.name ? -1 : a.def.name > b.def.name ? 1 : 0;
  });
  const winner = scored[0];

  // Materialise bounds in index-field order so the SQL planner sees the
  // leading eq first (helps it pick the b-tree).
  const bounds: IndexBound[] = [];
  for (let i = 0; i < winner.prefix; i++) {
    const field = winner.def.fields[i];
    bounds.push({ field, op: "eq", value: eqs.get(field) });
  }
  return { name: winner.def.name, bounds };
}
