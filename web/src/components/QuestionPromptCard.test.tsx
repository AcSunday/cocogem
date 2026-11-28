import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { statusText, terminal, type Question } from "../api/client";
import { QuestionPromptCard } from "./QuestionPromptCard";

describe("QuestionPromptCard & yielded run status", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("treats yielded run status as terminal with readable status label", () => {
    expect(terminal("yielded")).toBe(true);
    expect(statusText["yielded"]).toBeTruthy();
  });

  it("renders open questions with preselected recommended option and submits answer with If-Match", async () => {
    const question: Question = {
      id: "q-1",
      task_id: "task-1",
      task_revision: 3,
      blocking_scope: "task",
      body: "是否交给本轮名册中的「狸花猫」处理 `src/app.tsx:10`？",
      options: ["确认交给狸花猫", "暂不派发"],
      recommended: "确认交给狸花猫",
      status: "open",
      created_at: "2026-11-28T02:11:14Z",
    };
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ ...question, status: "resolved", answer: "暂不派发" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    const onAnswered = vi.fn();
    const openedFiles: Array<{ path: string; startLine?: number }> = [];

    render(
      <QuestionPromptCard
        questions={[question]}
        threadID="th-1"
        onOpenFile={(ref) => openedFiles.push(ref)}
        onAnswered={onAnswered}
      />,
    );

    expect(screen.getByTestId("conversation-questions")).toBeInTheDocument();
    const recBtn = screen.getByRole("button", { name: /确认交给狸花猫.*建议/ });
    expect(recBtn).toHaveAttribute("aria-pressed", "true");

    // Inline file link inside question body is clickable.
    fireEvent.click(screen.getByText("src/app.tsx:10"));
    expect(openedFiles).toEqual([{ path: "src/app.tsx", startLine: 10 }]);

    // Select second option and submit.
    fireEvent.click(screen.getByRole("button", { name: "暂不派发" }));
    fireEvent.click(screen.getByRole("button", { name: "确认并继续" }));

    await waitFor(() => expect(onAnswered).toHaveBeenCalledTimes(1));
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    const [url, init] = fetchSpy.mock.calls[0]!;
    expect(url).toBe("/api/v1/tasks/task-1/questions/q-1/answers");
    expect((init?.headers as Record<string, string>)["If-Match"]).toBe("3");
    expect(JSON.parse(String(init?.body))).toMatchObject({ answer: "暂不派发" });
  });
});
