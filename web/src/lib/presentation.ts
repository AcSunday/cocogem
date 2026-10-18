export const workflows = {
  free: "自由协作",
  discussion: "协作讨论",
  research: "Research",
  code_analysis: "代码分析",
};
export const errorText = (error: unknown) =>
  error instanceof Error ? error.message : "操作失败";
export const catIcon = (index: number) => ["◈", "✦", "●", "◐"][index % 4];
