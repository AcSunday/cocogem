import type { Artifact, Run } from "../api/client";

/**
 * Only a succeeded run with at least one authoritative reference may be
 * folded. A null delivery is legacy history and is never inferred.
 */
export function deliveredRun(run: Run): boolean {
  const delivery = run.delivery;
  return (
    run.status === "succeeded" &&
    !!delivery &&
    (delivery.message_ids?.length ?? 0) +
      (delivery.artifact_refs?.length ?? 0) >
      0
  );
}

export function deliveredMessageIDs(run: Run): Set<string> {
  return new Set(deliveredRun(run) ? (run.delivery?.message_ids ?? []) : []);
}

/** Final artifacts match the exact id and version, never a latest-version guess. */
export function deliveredArtifacts(
  run: Run,
  artifacts: Artifact[],
): Artifact[] {
  const scoped = artifacts.filter((artifact) => artifact.run_id === run.id);
  if (!deliveredRun(run)) return scoped;
  const refs = new Set(
    (run.delivery?.artifact_refs ?? []).map(
      (ref) => `${ref.id}@${ref.version}`,
    ),
  );
  return scoped.filter((artifact) =>
    refs.has(`${artifact.id}@${artifact.version}`),
  );
}

/** Final IDs absent from the loaded message page stay addressable, not guessed. */
export function missingDeliveryMessages(
  run: Run,
  messages: { id: string }[],
): string[] {
  if (!deliveredRun(run)) return [];
  const loaded = new Set(messages.map((message) => message.id));
  return (run.delivery?.message_ids ?? []).filter((id) => !loaded.has(id));
}
