import { afterEach, expect, it } from "vitest";
import { statusText } from "../api/client";
import { budgetFields, executionModes } from "../app/useGlobalSettings";
import {
  getLanguage,
  isEnglish,
  LANGUAGE_STORAGE_KEY,
  setLanguage,
  t,
  translateBackendMessage,
} from "./i18n";
import {
  artifactTypeLabel,
  bytesLabel,
  executionPolicyRows,
  failureKindLabel,
  formatRelativeTime,
  limitLabel,
  methodLabel,
  roleLabel,
  runDuration,
  runErrorText,
  runMethod,
  sourceKindLabel,
  toolLabel,
} from "./presentation";
import {
  taskColumns,
  taskFailureText,
  taskFocusFilters,
  taskFocusGroups,
  taskPhaseMeta,
  waitingLabels,
} from "./tasks";
import {
  activityPhrase,
  missingFinishLabel,
  toolStatusLabel,
  type ToolActivity,
} from "./toolActivity";
import type { ProjectTaskView } from "../api/client";

afterEach(() => {
  localStorage.clear();
  setLanguage("zh-CN");
});

it("defaults to zh-CN and switches cleanly to English", () => {
  expect(getLanguage()).toBe("zh-CN");
  expect(isEnglish()).toBe(false);
  expect(t("设置", "Settings")).toBe("设置");

  setLanguage("en");
  expect(getLanguage()).toBe("en");
  expect(isEnglish()).toBe(true);
  expect(localStorage.getItem(LANGUAGE_STORAGE_KEY)).toBe("en");
  expect(document.documentElement.lang).toBe("en");
  expect(t("设置", "Settings")).toBe("Settings");
});

it("adapts presentation, status, execution modes, budget fields and tasks to English", () => {
  setLanguage("en");
  expect(statusText.running).toBe("Running");
  expect(statusText.succeeded).toBe("Completed");
  expect(executionModes[0].label).toBe("Sandboxed");
  expect(executionModes[1].label).toBe("Full access");
  const [firstKey, firstLabel, firstUnit, firstDesc] = budgetFields[0];
  expect(firstKey).toBe("auto_continuations");
  expect(firstLabel).toBe("Auto-continuations");
  expect(firstUnit).toBe("runs");
  expect(firstDesc).toContain("Maximum automatic follow-up runs");

  expect(toolLabel("read_file")).toBe("Read file");
  expect(artifactTypeLabel("research_report")).toBe("Research Report");
  expect(roleLabel("researcher")).toBe("Research");
  expect(methodLabel("delegation")).toBe("Team delegation");
  expect(runMethod({ retry_of_step_id: "s1" }).label).toBe("Member retry");
  expect(bytesLabel(128)).toBe("128 bytes");
  expect(limitLabel("run_seconds", 0)).toBe(
    "No overall collaboration time limit",
  );
  expect(
    executionPolicyRows({
      run_timeout_ns: 0,
      step_timeout_ns: 0,
      first_response_ns: 0,
      stall_ns: 0,
      stall_action: "warn",
    })[0].label,
  ).toBe("Overall time limit");
  expect(sourceKindLabel({ kind: "web", url: "https://example.com" })).toBe(
    "Web page · Full text read",
  );
  expect(
    formatRelativeTime(
      new Date(2026, 9, 20, 17, 58).toISOString(),
      new Date(2026, 9, 20, 18, 0),
    ),
  ).toBe("2m ago");
  expect(
    runDuration({
      created_at: "2026-10-20T01:00:00Z",
      finished_at: "2026-10-20T01:01:05Z",
    }),
  ).toBe("1m 5s");
  expect(runErrorText("context canceled", "canceled")).toBe(
    "Stopped as requested.",
  );
  expect(failureKindLabel("network")).toContain("upstream endpoint");
  expect(translateBackendMessage("密码不正确")).toBe("Incorrect password.");
  expect(runErrorText("密码不正确")).toBe("Incorrect password.");

  expect(taskColumns[0].label).toBe("To do");
  expect(taskFocusGroups[0].label).toBe("Needs you");
  expect(taskFocusFilters[0].label).toBe("Active tasks");
  expect(waitingLabels.question).toBe("Waiting for your answer");
  const sampleTask = {
    task: {
      id: "t1",
      title: "Demo",
      status: "ready",
      waiting_reason: "",
    },
    revision: { goal: "Demo goal", acceptance: [] },
    last_run_status: "succeeded",
  } as unknown as ProjectTaskView;
  expect(taskPhaseMeta(sampleTask).label).toBe("Ready for review");
  expect(taskPhaseMeta(sampleTask).action.label).toBe("Review");
  expect(
    taskFailureText({
      ...sampleTask,
      last_run_status: "failed",
      last_run_failure_kind: "network",
    }),
  ).toBe("Could not connect to the local CLI endpoint");

  expect(toolStatusLabel("awaiting_permission")).toBe("Awaiting approval");
  expect(activityPhrase("read_file", "", 3)).toBe("Read 3 files");
  expect(
    missingFinishLabel({ unknownReason: "run_ended" } as ToolActivity),
  ).toBe("No completion record · Run ended");
});
