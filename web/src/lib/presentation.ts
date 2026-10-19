import type { Agent } from "../api/client";

export const workflows = {
  free: "自由协作",
  discussion: "协作讨论",
  research: "Research",
  code_analysis: "代码分析",
};
export const errorText = (error: unknown) =>
  error instanceof Error ? error.message : "操作失败";
export const catIcon = (index: number) => ["◈", "✦", "●", "◐"][index % 4];
export const memberLabel = (agent: Agent) =>
  `${agent.display_name} (@${agent.handle})`;

export function defaultAssignments(agents: Agent[], coordinatorID = "") {
  const enabled = agents.filter((agent) => !agent.disabled);
  return {
    researcher: enabled[0]?.id ?? "",
    critic: enabled[1]?.id ?? "",
    coordinator:
      enabled.find((agent) => agent.id === coordinatorID)?.id ??
      enabled[2]?.id ??
      enabled[0]?.id ??
      "",
    analyst: enabled[0]?.id ?? "",
    reviewer: enabled[1]?.id ?? "",
  };
}
