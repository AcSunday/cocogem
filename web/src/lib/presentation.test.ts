import { expect, it } from "vitest";
import type { Agent } from "../api/client";
import { defaultAssignments } from "./presentation";

it("defaults roles to enabled conversation members and its coordinator", () => {
  const agents: Agent[] = ["disabled", "one", "two"].map((id) => ({
    id,
    workspace_id: "ws",
    display_name: id,
    handle: id,
    aliases: [],
    role_description: "",
    model: "demo",
    tools: [],
    disabled: id === "disabled",
    revision: 1,
  }));
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
