export type StyleModule = Record<string, string>;

/**
 * Builds a class resolver restricted to the CSS modules a component declares.
 * A name used by several declared modules (e.g. "open", "error") maps to
 * every declared module's class so compound selectors like `.sidebar.open`
 * still match; names unknown to the declared modules pass through unchanged.
 *
 * Declaring the modules per file keeps a new generic class in an unrelated
 * module from leaking into components that never asked for it.
 */
export function scoped(...modules: StyleModule[]) {
  return (value: string): string =>
    value
      .split(/\s+/)
      .filter(Boolean)
      .map((name) => {
        const hits = modules.map((m) => m[name]).filter(Boolean);
        return hits.length ? hits.join(" ") : name;
      })
      .join(" ");
}
