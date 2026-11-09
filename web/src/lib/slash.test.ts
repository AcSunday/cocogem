import { expect, it } from "vitest";
import { applyMention, mentionAt } from "./mention";
import { applySlash, methodLabel, slashAt, slashMatches } from "./slash";

it("detects a slash command only at a word start", () => {
  expect(slashAt("/", 1)).toEqual({ start: 0, query: "" });
  expect(slashAt("请 /研", 4)).toEqual({ start: 2, query: "研" });
  expect(slashAt("https://example.org", 18)).toBeNull();
  expect(slashAt("a/b", 3)).toBeNull();
  expect(slashAt("/工程 后面的正文", 3)).toEqual({ start: 0, query: "工程" });
});

it("matches commands by label, key and keyword", () => {
  expect(slashMatches("").map((item) => item.key)).toEqual([
    "free",
    "discussion",
    "research",
    "engineering",
  ]);
  expect(slashMatches("研").map((item) => item.key)).toEqual(["research"]);
  expect(slashMatches("工程").map((item) => item.key)).toEqual(["engineering"]);
  expect(slashMatches("missing")).toEqual([]);
});

it("removes the command from the draft and keeps the caret", () => {
  expect(applySlash("/研究 请分析这份材料", 0, 3)).toEqual({
    text: "请分析这份材料",
    caret: 0,
  });
  expect(applySlash("请分析 /研究", 4, 7)).toEqual({
    text: "请分析",
    caret: 3,
  });
  expect(methodLabel("free")).toBe("");
  expect(methodLabel("research")).toBe("研究协作");
});

it("keeps the existing mention behaviour unchanged", () => {
  expect(mentionAt("请 @ra", 5)).toEqual({ start: 2, query: "ra" });
  expect(applyMention("请 @ra 看", 2, 5, "ragdoll")).toEqual({
    text: "请 @ragdoll  看",
    caret: 11,
  });
});
