import { expect, it } from "vitest";
import type { ProjectTaskView, Question } from "../api/client";
import {
  taskAcceptance,
  taskColumn,
  taskFailureText,
  taskFocus,
  taskFocusMeta,
  taskGoalSummary,
  taskPhase,
  taskTitle,
} from "./tasks";

const questions: Question[] = [
  {
    id: "question",
    task_id: "task",
    task_revision: 1,
    blocking_scope: "范围",
    body: "是否包含反证？",
    options: ["包含"],
    recommended: "包含",
    status: "open",
    created_at: "2026-11-04T00:00:00Z",
  },
];

function view(
  task: Partial<ProjectTaskView["task"]> = {},
  lastRunStatus?: string,
): ProjectTaskView {
  return {
    task: {
      id: "task",
      title: "订单幂等改造",
      workspace_id: "ws",
      thread_id: "thread",
      current_revision: 1,
      owner_member_id: "owner",
      owner_revision: 1,
      root_task_id: "task",
      root_control_epoch: 1,
      budget_account_id: "budget",
      collaboration_scope_id: "scope",
      status: "ready",
      priority: 0,
      evaluation_generation: 1,
      evaluation_status: "incomplete",
      created_at: "2026-11-04T00:00:00Z",
      updated_at: "2026-11-04T00:00:00Z",
      ...task,
    },
    revision: {
      task_id: "task",
      revision: 1,
      goal: "让重试保持幂等",
      scope: [],
      non_goals: [],
      constraint_refs: [],
      acceptance: [],
      input_refs: [],
      allowed_actions: [],
      source_message_id: "message",
      accepted_by: "user",
      acceptance_evidence: "message",
      created_at: "2026-11-04T00:00:00Z",
      digest: "digest",
    },
    budget: {
      id: "budget",
      root_task_id: "task",
      policy: {
        auto_continuations: 20,
        parallel_branches: 4,
        writer_branches: 2,
        fix_rounds: 3,
        no_progress_failures: 2,
      },
      sources: {
        auto_continuations: "global",
        parallel_branches: "global",
        writer_branches: "global",
        fix_rounds: "global",
        no_progress_failures: "global",
      },
      revision: 1,
      used_continuations: 0,
      reserved_runs: 0,
      active_branches: 0,
      active_writers: 0,
      fix_rounds_by_issue: {},
      no_progress_by_branch: {},
      updated_at: "2026-11-04T00:00:00Z",
    },
    completions: [],
    last_run_status: lastRunStatus,
  };
}

it("puts questions and failures in the attention stream with a real next action", () => {
  expect(taskPhase(view({ status: "active" }, "running"), questions)).toBe(
    "needs_answer",
  );
  expect(
    taskFocusMeta(view({ status: "active" }, "running"), questions),
  ).toMatchObject({
    focus: "attention",
    label: "需要你",
    current: "等待你回答：是否包含反证？",
    next: "回答后继续",
    action: { kind: "answer", label: "回答问题" },
  });

  expect(taskPhase(view({ status: "ready" }, "failed"))).toBe("needs_retry");
  expect(taskFocusMeta(view({ status: "ready" }, "failed"))).toMatchObject({
    focus: "attention",
    label: "执行失败",
    tone: "failed",
    next: "只补跑未完成步骤",
    action: { kind: "retry", label: "从停止处继续" },
  });

  expect(taskPhase(view({ waiting_reason: "uncertain_effect" }))).toBe(
    "needs_retry",
  );
  expect(
    taskFocusMeta(view({ waiting_reason: "uncertain_effect" })),
  ).toMatchObject({
    current: "上次执行可能产生了未核实结果",
    next: "先核实副作用再继续",
  });
});

it("treats a canceled run as stopped work, not a waiting condition", () => {
  const stopped = view({ status: "ready" }, "canceled");
  expect(taskPhase(stopped)).toBe("needs_retry");
  expect(taskFocus(stopped)).toBe("attention");
  expect(taskColumn(stopped)).toBe("waiting");
  expect(taskFocusMeta(stopped)).toMatchObject({
    label: "执行已停止",
    tone: "paused",
    current: "上次执行在完成前停止",
    next: "只补跑未完成步骤",
    action: { kind: "retry", label: "从停止处继续" },
  });
});

it("distinguishes running, paused, not started, budget and other waits", () => {
  expect(taskPhase(view({ status: "active" }, "running"))).toBe("running");
  expect(taskFocusMeta(view({ status: "active" }, "running"))).toMatchObject({
    focus: "running",
    label: "进行中",
    current: "正在执行",
    action: { kind: "open_execution", label: "查看执行过程" },
  });

  expect(taskPhase(view({ status: "paused" }, "running"))).toBe("paused");
  expect(taskFocusMeta(view({ status: "paused" }, "running"))).toMatchObject({
    focus: "waiting",
    label: "已暂停",
    action: { kind: "resume", label: "恢复任务" },
  });

  expect(taskPhase(view())).toBe("not_started");
  expect(taskFocusMeta(view())).toMatchObject({
    focus: "waiting",
    column: "todo",
    label: "待开始",
    current: "尚未开始",
    next: "创建会话并开始执行",
    action: { kind: "start", label: "开始执行" },
  });

  expect(taskPhase(view({ waiting_reason: "budget" }))).toBe("waiting_budget");
  expect(taskFocusMeta(view({ waiting_reason: "budget" }))).toMatchObject({
    focus: "waiting",
    label: "等待预算",
    action: { kind: "budget", label: "调整预算" },
  });

  expect(taskPhase(view({ waiting_reason: "dependency" }))).toBe(
    "waiting_other",
  );
  expect(taskFocusMeta(view({ waiting_reason: "dependency" }))).toMatchObject({
    label: "等待依赖任务",
    action: { kind: "wait", label: "查看任务" },
  });
});

it("treats a successful run as review and a completed task as archived", () => {
  expect(taskPhase(view({ status: "ready" }, "succeeded"))).toBe("review");
  expect(taskFocusMeta(view({ status: "ready" }, "succeeded"))).toMatchObject({
    focus: "review",
    label: "待验收",
    current: "已交付，等待验收",
    action: { kind: "review", label: "去验收" },
  });

  expect(
    taskPhase(view({ status: "completed" }, "succeeded")),
  ).toBe("completed");
  expect(taskFocusMeta(view({ status: "completed" }, "succeeded"))).toMatchObject({
    focus: "completed",
    label: "已验收",
    action: { kind: "view", label: "查看成果" },
  });

  expect(taskPhase(view({ status: "canceled" }))).toBe("canceled");
  expect(taskFocusMeta(view({ status: "canceled" }))).toMatchObject({
    focus: "canceled",
    label: "已取消",
  });
});

it("surfaces revalidation instead of claiming a completed task is accepted", () => {
  const revalidating = view({
    status: "completed",
    evaluation_status: "needs_revalidation",
  });
  expect(taskPhase(revalidating)).toBe("needs_revalidation");
  expect(taskFocusMeta(revalidating)).toMatchObject({
    focus: "review",
    column: "review",
    label: "待复验",
    action: { kind: "revalidate", label: "去复验" },
  });
});

it("explains a failed run in plain language", () => {
  const failed = view({}, "failed");
  failed.last_run_failure_kind = "network";
  expect(taskFailureText(failed)).toBe("无法连接本机 CLI 的端点");

  const unknown = view({}, "failed");
  unknown.last_run_error =
    "network: native CLI did not confirm success; check local network";
  expect(taskFailureText(unknown)).toBe("无法连接本机 CLI 的端点");

  expect(taskFailureText(view({}, "canceled"))).toBe(
    "上次执行在完成前停止",
  );
});

it("reads acceptance progress from the exact current completion", () => {
  const current = view({ status: "completed" });
  current.revision.acceptance = [
    {
      criterion_id: "one",
      statement: "第一条",
      required: true,
      kind: "outcome",
      evidence_requirements: [],
      evaluation_policy: "all_required",
    },
    {
      criterion_id: "two",
      statement: "第二条",
      required: true,
      kind: "outcome",
      evidence_requirements: [],
      evaluation_policy: "all_required",
    },
  ];
  current.completions = [
    {
      id: "old",
      task_id: "task",
      task_revision: 1,
      evaluation_generation: 1,
      input_snapshot_digest: "digest",
      judge_member_id: "user",
      evidence_producer: "human_confirmed",
      candidate_refs: [],
      results: [
        { criterion_id: "one", result: "passed", evidence: [], limitations: [] },
      ],
      unresolved: [],
      validity: "superseded",
      subject_digest: "subject",
      created_at: "2026-11-04T00:00:00Z",
    },
    {
      id: "current",
      task_id: "task",
      task_revision: 1,
      evaluation_generation: 1,
      input_snapshot_digest: "digest",
      judge_member_id: "user",
      evidence_producer: "human_confirmed",
      candidate_refs: [],
      results: [
        { criterion_id: "one", result: "passed", evidence: [], limitations: [] },
        { criterion_id: "two", result: "inconclusive", evidence: [], limitations: [] },
      ],
      unresolved: ["第二条"],
      validity: "current",
      subject_digest: "subject",
      created_at: "2026-11-04T00:00:00Z",
    },
  ];
  expect(taskAcceptance(current)).toEqual({ passed: 1, total: 2 });
});

it("does not count a completion from another evaluation generation", () => {
  const current = view({ status: "completed" });
  current.revision.acceptance = [
    {
      criterion_id: "one",
      statement: "第一条",
      required: true,
      kind: "outcome",
      evidence_requirements: [],
      evaluation_policy: "all_required",
    },
  ];
  current.task.evaluation_generation = 2;
  current.completions = [
    {
      id: "old",
      task_id: "task",
      task_revision: 1,
      evaluation_generation: 1,
      input_snapshot_digest: "digest",
      judge_member_id: "user",
      evidence_producer: "human_confirmed",
      candidate_refs: [],
      results: [
        { criterion_id: "one", result: "passed", evidence: [], limitations: [] },
      ],
      unresolved: [],
      validity: "current",
      subject_digest: "subject",
      created_at: "2026-11-04T00:00:00Z",
    },
  ];
  expect(taskAcceptance(current)).toEqual({ passed: 0, total: 1 });
});

it("uses the title and falls back to the first goal line", () => {
  expect(taskTitle(view())).toBe("订单幂等改造");
  const untitled = view({ title: "" });
  untitled.revision.goal = "第一行\n第二行";
  expect(taskTitle(untitled)).toBe("第一行");
  expect(taskGoalSummary(untitled.revision.goal)).toBe("第一行 第二行");
});
