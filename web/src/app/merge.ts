// Pure merge helpers for the workspace store: HTTP snapshots, SSE frames and
// paged history all converge here. A record only moves forward; a stale page
// must not revive or drop anything.
import {
  terminal,
  type Attempt,
  type Event,
  type Run,
  type Step,
  type ThreadView,
} from "../api/client";
import { mergeMessage } from "../api/client";

const stepTerminal = new Set([
  "succeeded",
  "failed",
  "canceled",
  "interrupted",
  "skipped",
]);
const attemptTerminal = new Set([
  "succeeded",
  "failed",
  "canceled",
  "interrupted",
  "uncertain",
  "blocked",
]);

/** A persisted run only moves forward; a stale HTTP snapshot must not revive it. */
export function mergeRun(current: Run, incoming: Run): Run {
  return terminal(current.status) && !terminal(incoming.status)
    ? current
    : incoming;
}

export function mergeStep(current: Step, incoming: Step): Step {
  return stepTerminal.has(current.status) && !stepTerminal.has(incoming.status)
    ? current
    : incoming;
}

export function mergeAttempt(current: Attempt, incoming: Attempt): Attempt {
  return attemptTerminal.has(current.status) &&
    !attemptTerminal.has(incoming.status)
    ? current
    : incoming;
}

/**
 * Steps and attempts are append-only within a run, and an event-history page
 * can be a partial window. Merge into the known items instead of replacing
 * them, so a bounded seed or a stale snapshot never drops or repeats records.
 */
export function mergeByID<T extends { id: string }>(
  current: T[] | undefined,
  incoming: T[],
  merge: (current: T, incoming: T) => T,
) {
  const items = new Map<string, T>();
  for (const item of current ?? []) if (item.id) items.set(item.id, item);
  for (const item of incoming) {
    if (!item.id) continue;
    const previous = items.get(item.id);
    items.set(item.id, previous ? merge(previous, item) : item);
  }
  return [...items.values()];
}

export function withRunWarnings(
  current: Record<string, Event[]>,
  runID: string,
  warnings: Event[],
) {
  if (!warnings.length) {
    if (!(runID in current)) return current;
    const copy = { ...current };
    delete copy[runID];
    return copy;
  }
  return current[runID] === warnings
    ? current
    : { ...current, [runID]: warnings };
}

/**
 * A refresh may return a snapshot started before the last SSE frame. Merge
 * message bodies by id and keep runs terminal, so a reload cannot move the
 * loaded page backwards.
 */
export function mergeThreadView(
  current: ThreadView | undefined,
  loaded: ThreadView,
  samePage: boolean,
  startConfirmed: Record<string, number>,
  messageWatermark: Record<string, number>,
): ThreadView {
  if (!current || current.thread.id !== loaded.thread.id) return loaded;
  const known = new Map(current.messages.items.map((item) => [item.id, item]));
  const seen = new Set<string>();
  const items = loaded.messages.items.map((message) => {
    seen.add(message.id);
    const previous = known.get(message.id);
    if (!previous) return message;
    // Only a message event that landed after this request started can be
    // newer than the consistent snapshot the server just returned.
    const applied = messageWatermark[message.id] ?? 0;
    const base = startConfirmed[message.run_id] ?? 0;
    return applied > base ? mergeMessage(previous, message) : message;
  });
  if (samePage)
    for (const message of current.messages.items) {
      if (seen.has(message.id)) continue;
      const applied = messageWatermark[message.id] ?? 0;
      const base = startConfirmed[message.run_id] ?? 0;
      if (applied > base) items.push(message);
    }
  return {
    ...loaded,
    messages: { ...loaded.messages, items },
    runs: loaded.runs.map((run) => {
      const previous = current.runs.find((item) => item.id === run.id);
      return previous ? mergeRun(previous, run) : run;
    }),
  };
}

const eventObject = (event: Event) =>
  event.data && typeof event.data === "object"
    ? (event.data as Record<string, unknown>)
    : {};

export function eventRun(event: Event) {
  if (!event.type.startsWith("run_")) return undefined;
  const data = eventObject(event);
  return typeof data.id === "string" && typeof data.status === "string"
    ? (data as unknown as Run)
    : undefined;
}

export function eventStep(event: Event) {
  if (!event.type.startsWith("step_")) return undefined;
  const data = eventObject(event);
  return typeof data.id === "string" &&
    data.id !== "" &&
    typeof data.status === "string"
    ? (data as unknown as Step)
    : undefined;
}

export function eventAttempt(event: Event) {
  if (!event.type.startsWith("attempt_")) return undefined;
  const data = eventObject(event);
  return typeof data.id === "string" &&
    data.id !== "" &&
    typeof data.status === "string"
    ? (data as unknown as Attempt)
    : undefined;
}
