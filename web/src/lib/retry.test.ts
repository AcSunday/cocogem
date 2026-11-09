import { expect, it } from "vitest";
import type { Run, Step } from "../api/client";
import {
  failedMemberRetries,
  latestTurnRunID,
  outstandingStepFailures,
  runTurns,
} from "./retry";

type RunExtra = Omit<Partial<Run>, "input"> & {
  input?: Partial<Run["input"]>;
};

const run = (extra: RunExtra = {}): Run => {
  const { input: inputExtra, ...rest } = extra;
  const input = {
    workflow: "free",
    retry_of_run_id: "",
    ...inputExtra,
  } as Run["input"];
  return { id: "run", status: "failed", ...rest, input } as Run;
};

it("keeps another failed member available after one member retry succeeds", () => {
  const source = run({ id: "source" });
  const repaired = run({
    id: "repair",
    status: "succeeded",
    input: { retry_of_run_id: source.id },
    retry_of_step_id: "first",
  });
  const first = { id: "first", status: "failed" } as Step;
  const second = { id: "second", status: "failed" } as Step;
  expect(
    failedMemberRetries([source, repaired], { source: [first, second] }),
  ).toEqual([{ runID: "source", step: second }]);
});

it("shows the newest failed attempt and does not repeat its source failure", () => {
  const source = run({ id: "source" });
  const repaired = run({
    id: "repair",
    input: { retry_of_run_id: source.id },
    retry_of_step_id: "first",
  });
  const first = { id: "first", status: "failed" } as Step;
  const retryStep = { id: "retry-step", status: "failed" } as Step;
  expect(
    failedMemberRetries([source, repaired], {
      source: [first],
      repair: [retryStep],
    }),
  ).toEqual([{ runID: "repair", step: retryStep }]);
  const next = run({ id: "next-user-turn", status: "succeeded" });
  expect(
    failedMemberRetries([source, repaired, next], {
      source: [first],
      repair: [retryStep],
    }),
  ).toEqual([]);
});

it("groups a member retry under the user turn it replaces", () => {
  const first = run({ id: "first" });
  const second = run({ id: "second" });
  const repair = run({
    id: "repair",
    status: "succeeded",
    input: { retry_of_run_id: first.id },
    retry_of_step_id: "member",
  });
  expect(runTurns([first, second, repair])).toEqual([
    { root: first, runs: [first, repair] },
    { root: second, runs: [second] },
  ]);
  expect(runTurns([first, repair]).map((turn) => turn.root.id)).toEqual([
    "first",
  ]);
});

it("keeps every failure in a retry chain addressable until it is replaced", () => {
  const source = run({ id: "source" });
  const repair = run({
    id: "repair",
    input: { retry_of_run_id: source.id },
    retry_of_step_id: "first",
  });
  const first = { id: "first", status: "failed" } as Step;
  const second = { id: "second", status: "failed" } as Step;
  const retryStep = { id: "retry-step", status: "failed" } as Step;
  expect(
    outstandingStepFailures([source, repair], {
      source: [first, second],
      repair: [retryStep],
    }),
  ).toEqual([
    { runID: "source", step: second },
    { runID: "repair", step: retryStep },
  ]);
  expect(latestTurnRunID([source, repair])).toBe("source");
});
