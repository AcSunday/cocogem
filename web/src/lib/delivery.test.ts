import { expect, it } from "vitest";
import type { Artifact, Run } from "../api/client";
import { deliveredArtifacts, deliveredMessageIDs } from "./delivery";

const run = {
  id: "run",
  status: "succeeded",
  delivery: {
    message_ids: ["final-message"],
    artifact_refs: [{ id: "final-report", version: 1 }],
  },
} as Run;

const artifact = (
  id: string,
  version: number,
  stepID: string,
  type = "code_analysis",
): Artifact =>
  ({
    id,
    version,
    revision: 1,
    thread_id: "thread",
    run_id: "run",
    step_id: stepID,
    type,
    title: `${type} v${version}`,
    body: "body",
    findings: [],
    source_ids: [],
    limitations: [],
    feedback: "unreviewed",
    feedback_at: "",
    created_at: "2026-10-20T00:00:00Z",
  }) as Artifact;

it("delivers only the designated final artifact among five plan steps", () => {
  const artifacts = [
    artifact("intermediate-note", 1, "analysis", "note"),
    artifact("final-report", 1, "final_review"),
    artifact("final-report", 2, "final_review"),
  ];
  const delivered = deliveredArtifacts(run, artifacts);
  expect(delivered).toHaveLength(1);
  expect(delivered[0].id).toBe("final-report");
  expect(delivered[0].version).toBe(1);
  expect(deliveredMessageIDs(run).has("final-message")).toBe(true);
  expect(deliveredMessageIDs(run).has("intermediate-message")).toBe(false);
});

it("does not infer a delivery for legacy or failed runs", () => {
  const artifacts = [artifact("final-report", 1, "final_review")];
  expect(deliveredArtifacts({ ...run, delivery: null } as Run, artifacts)).toHaveLength(1);
  expect(
    deliveredArtifacts({ ...run, status: "failed" } as Run, artifacts),
  ).toHaveLength(1);
  expect(deliveredMessageIDs({ ...run, delivery: null } as Run).size).toBe(0);
});
