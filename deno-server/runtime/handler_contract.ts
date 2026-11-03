// EXC-640: what a deploy is refused with when its module has nothing to run.
// The default export must be a (req: Request) => Response handler, or a
// tagged v2 export where those are enabled. Kept free of outer references:
// the worker prelude embeds its source.
export function handlerContractRefusal(exported: unknown, exportNames: string[], v2Export: boolean): string | null {
  if (typeof exported === "function" || v2Export) return null;
  const named = exportNames.filter((name) => name !== "default");
  let found = "found no exports";
  if (exported !== undefined) {
    found = "the default export is a " + (exported === null ? "null" : typeof exported) + ", not a function";
  } else if (named.length > 0) {
    found = "found only named exports: " + named.join(", ");
  }
  return "index.ts must use export default (req: Request) => Response; " + found;
}
