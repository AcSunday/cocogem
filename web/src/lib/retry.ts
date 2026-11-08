import type { Run, Step } from "../api/client";

export type RetryRef = { runID: string; step: Step };

export type RunTurn = {
  root: Run;
  runs: Run[];
};

/** The root run whose user input started this retry chain. */
export function rootRunID(run: Run, runs: Run[]): string {
  const byID = new Map(runs.map((item) => [item.id, item]));
  const seen = new Set<string>();
  while (run.input.retry_of_run_id && !seen.has(run.id)) {
    seen.add(run.id);
    const previous = byID.get(run.input.retry_of_run_id);
    if (!previous) break;
    run = previous;
  }
  return run.id;
}

/** Groups member retries under the user turn they replace instead of adding a new turn. */
export function runTurns(runs: Run[]): RunTurn[] {
  const groups = new Map<string, Run[]>();
  for (const run of runs) {
    const root = rootRunID(run, runs);
    const group = groups.get(root) ?? [];
    group.push(run);
    groups.set(root, group);
  }
  return runs
    .filter((run) => run.id === rootRunID(run, runs))
    .map((root) => ({ root, runs: groups.get(root.id) ?? [root] }));
}

/** The newest failed step that has not already been replaced by a retry run. */
export function outstandingStepFailures(
  runs: Run[],
  steps: Record<string, Step[]>,
): RetryRef[] {
  return runs.flatMap((run) =>
    (steps[run.id] ?? [])
      .filter(
        (step) =>
          ["failed", "canceled"].includes(step.status) &&
          !runs.some(
            (retry) =>
              retry.input.retry_of_run_id === run.id &&
              retry.retry_of_step_id === step.id,
          ),
      )
      .map((step) => ({ runID: run.id, step })),
  );
}

/** Outstanding failures in the latest user turn, including its member retries. */
export function failedMemberRetries(
  runs: Run[],
  steps: Record<string, Step[]>,
): RetryRef[] {
  const latest = runs.at(-1);
  if (!latest) return [];
  const rootID = rootRunID(latest, runs);
  return outstandingStepFailures(
    runs.filter((run) => rootRunID(run, runs) === rootID),
    steps,
  );
}

/** A notice belongs to one accepted user turn, not to each retry attempt. */
export function latestTurnRunID(runs: Run[]): string {
  const latest = runs.at(-1);
  return latest ? rootRunID(latest, runs) : "";
}
