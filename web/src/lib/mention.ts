import type { Agent } from "../api/client";

/** The "@query" being typed right before the caret, if any. */
export function mentionAt(text: string, caret: number) {
  const match = text.slice(0, caret).match(/(^|\s)@([^\s@]*)$/);
  if (!match) return null;
  return { start: caret - match[2].length - 1, query: match[2].toLowerCase() };
}

export function mentionMatches(agents: Agent[], query: string) {
  return agents.filter((agent) =>
    [agent.handle, agent.display_name, ...(agent.aliases ?? [])].some((name) =>
      name.toLowerCase().startsWith(query),
    ),
  );
}

/** Replace the typed "@query" with the member's handle and place the caret after it. */
export function applyMention(
  text: string,
  start: number,
  caret: number,
  handle: string,
) {
  const inserted = `@${handle} `;
  return {
    text: text.slice(0, start) + inserted + text.slice(caret),
    caret: start + inserted.length,
  };
}
