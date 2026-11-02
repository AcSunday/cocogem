import {
  eventMessage,
  mergeEvents,
  type Attempt,
  type Event,
  type Message,
  type Run,
  type Step,
} from "../api/client";
import {
  eventAttempt,
  eventRun,
  eventStep,
  mergeAttempt,
  mergeRun,
  mergeStep,
} from "./merge";

/** One replay window projected into the objects the conversation renders. */
export type RunWindow = {
  events: Event[];
  messages: { seq: number; message: Message }[];
  steps: Step[];
  attempts: Attempt[];
  run?: Run;
  newest: number;
};

/**
 * Folds a page of run events into exactly one projection. The function is pure
 * so the replay rules can be tested without mounting the conversation.
 */
export function collectRunWindow(items: Event[], runID: string): RunWindow {
  let newest = 0;
  let events: Event[] = [];
  const messages: { seq: number; message: Message }[] = [];
  const steps = new Map<string, Step>();
  const attempts = new Map<string, Attempt>();
  let run: Run | undefined;
  for (const event of items) {
    newest = Math.max(newest, event.seq);
    events = mergeEvents(events, event);
    if (event.run_id !== runID) continue;
    const message = eventMessage(event);
    if (message && message.run_id === runID)
      messages.push({ seq: event.seq, message });
    const nextRun = eventRun(event);
    if (nextRun?.id === runID) run = run ? mergeRun(run, nextRun) : nextRun;
    const step = eventStep(event);
    if (step?.run_id === runID) {
      const previous = steps.get(step.id);
      steps.set(step.id, previous ? mergeStep(previous, step) : step);
    }
    const attempt = eventAttempt(event);
    if (attempt?.run_id === runID) {
      const previous = attempts.get(attempt.id);
      attempts.set(
        attempt.id,
        previous ? mergeAttempt(previous, attempt) : attempt,
      );
    }
  }
  return {
    events,
    messages,
    steps: [...steps.values()],
    attempts: [...attempts.values()],
    run,
    newest,
  };
}
