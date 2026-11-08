import { workflowChoices, workflows } from "./presentation";
import type { WorkflowKey } from "../components/Composer";

/** One "/" command. `key` is the persisted workflow value. */
export type WorkflowCommand = {
  key: WorkflowKey;
  label: string;
  keywords: string[];
};

const commandKeywords: Record<string, string[]> = {
  free: ["自由", "自动", "free", "auto"],
  discussion: ["讨论", "discussion"],
  research: ["研究", "research"],
  engineering: ["工程", "engineering", "代码"],
};

export const workflowCommands: WorkflowCommand[] = workflowChoices.map(
  ([key, label]) => ({
    key: key as WorkflowKey,
    label,
    keywords: commandKeywords[key] ?? [],
  }),
);

/** The "/query" being typed right before the caret, if any. */
export function slashAt(text: string, caret: number) {
  const match = text.slice(0, caret).match(/(^|\s)\/([^\s/]*)$/);
  if (!match) return null;
  return { start: caret - match[2].length - 1, query: match[2].toLowerCase() };
}

export function slashMatches(query: string) {
  const needle = query.trim().toLowerCase();
  if (!needle) return workflowCommands;
  return workflowCommands.filter((command) =>
    [command.label, command.key, ...command.keywords].some((name) =>
      name.toLowerCase().startsWith(needle),
    ),
  );
}

/**
 * Remove the typed "/query" so the draft keeps the user's own wording. One
 * separator space before or after the command goes with it, so the remaining
 * text keeps its original shape instead of gaining a leading blank.
 */
export function applySlash(text: string, start: number, caret: number) {
  let from = start;
  if (from > 0 && /\s/.test(text[from - 1])) from -= 1;
  let to = caret;
  if (/\s/.test(text[to] ?? "") && (from === 0 || /\s/.test(text[from] ?? ""))) {
    to += 1;
  }
  return { text: text.slice(0, from) + text.slice(to), caret: from };
}

/** The label shown for the chosen method; the default needs no chip. */
export function methodLabel(workflow: string) {
  return workflow === "free"
    ? ""
    : (workflows[workflow as keyof typeof workflows] ?? "");
}
