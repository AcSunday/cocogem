import base from "./base.module.css";
import controls from "./controls.module.css";
import layout from "./layout.module.css";
import conversation from "./conversation.module.css";
import panels from "./panels.module.css";

const modules: Record<string, string>[] = [
  base,
  controls,
  layout,
  conversation,
  panels,
];

// Resolve the shared workspace design vocabulary to locally scoped CSS classes.
// A name used by several modules (e.g. "open", "error") maps to every module's
// class so compound selectors like `.sidebar.open` match in each file.
export function cx(value: string): string {
  return value
    .split(/\s+/)
    .filter(Boolean)
    .map((name) => {
      const hits = modules.map((m) => m[name]).filter(Boolean);
      return hits.length ? hits.join(" ") : name;
    })
    .join(" ");
}
