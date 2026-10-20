import { expect, it } from "vitest";
import type { Agent, Source } from "../api/client";
import {
  defaultAssignments,
  formatMessageTime,
  latestSources,
  runErrorText,
  runDuration,
  threadTitle,
} from "./presentation";
import { applyMention, mentionAt, mentionMatches } from "./mention";

const agent = (id: string, extra: Partial<Agent> = {}): Agent => ({
  id,
  workspace_id: "ws",
  display_name: id,
  handle: id,
  aliases: [],
  role_description: "",
  model: "demo",
  tools: [],
  disabled: false,
  revision: 1,
  ...extra,
});

it("defaults roles to enabled conversation members and its coordinator", () => {
  const agents = ["disabled", "one", "two"].map((id) =>
    agent(id, { disabled: id === "disabled" }),
  );
  expect(defaultAssignments(agents, "two")).toEqual({
    researcher: "one",
    critic: "two",
    coordinator: "two",
    analyst: "one",
    reviewer: "two",
  });
  expect(defaultAssignments([], "missing")).toEqual({
    researcher: "",
    critic: "",
    coordinator: "",
    analyst: "",
    reviewer: "",
  });
});

it("completes a mention typed anywhere in the draft", () => {
  const text = "请 @ma 复核";
  const at = mentionAt(text, 5);
  expect(at).toEqual({ start: 2, query: "ma" });
  expect(mentionAt("mail@ma", 7)).toBeNull();
  const agents = [
    agent("maine", { display_name: "缅因猫" }),
    agent("ragdoll", { display_name: "布偶猫", aliases: ["布布"] }),
  ];
  expect(mentionMatches(agents, "ma").map((a) => a.id)).toEqual(["maine"]);
  expect(mentionMatches(agents, "布").map((a) => a.id)).toEqual(["ragdoll"]);
  expect(applyMention(text, at!.start, 5, "maine")).toEqual({
    text: "请 @maine  复核",
    caret: 9,
  });
});

it("shortens titles, dates older messages and keeps one capture per source", () => {
  expect(threadTitle("研究多 Agent 协作的适用场景，说明证据和限制。补充")).toBe(
    "研究多 Agent 协作的适用场景，说明证据和限制",
  );
  expect([...threadTitle("一".repeat(40))]).toHaveLength(29);
  const now = new Date(2026, 9, 20, 18, 0);
  expect(
    formatMessageTime(new Date(2026, 9, 20, 9, 5).toISOString(), now),
  ).toBe("09:05");
  expect(
    formatMessageTime(new Date(2026, 9, 18, 9, 5).toISOString(), now),
  ).toBe("10月18日 09:05");
  expect(
    runDuration({
      created_at: "2026-10-20T01:00:00Z",
      finished_at: "2026-10-20T01:01:05Z",
    }),
  ).toBe("1 分 5 秒");
  const source = (id: string, url: string) =>
    ({ id, kind: "web", url, path: "" }) as Source;
  expect(
    latestSources([
      source("a1", "https://a"),
      source("b1", "https://b"),
      source("a2", "https://a"),
    ]).map((s) => s.id),
  ).toEqual(["a2", "b1"]);
  expect(runErrorText("context canceled", "canceled")).toBe(
    "已按你的要求停止。",
  );
  expect(runErrorText("upstream step did not succeed")).toBe(
    "上游步骤没有完成，这一步已跳过。",
  );
  expect(runErrorText("CLI 退出码 2")).toBe("CLI 退出码 2");
});
