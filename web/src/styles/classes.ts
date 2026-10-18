import styles from "./workspace.module.css";

// Resolve the shared workspace design vocabulary to locally scoped CSS classes.
export function cx(value: string): string {
  return value
    .split(/\s+/)
    .map((name) => styles[name] ?? name)
    .join(" ");
}
