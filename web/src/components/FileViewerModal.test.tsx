import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FileViewerModal } from "./FileViewerModal";
import { MessageCard } from "./MessageCard";
import type { Message, Source } from "../api/client";

describe("FileViewerModal & visual evidence thumbnails", () => {
  beforeEach(() => {
    HTMLDialogElement.prototype.showModal = vi.fn(function (
      this: HTMLDialogElement,
    ) {
      this.open = true;
      this.setAttribute("open", "");
    });
    HTMLDialogElement.prototype.close = vi.fn(function (
      this: HTMLDialogElement,
    ) {
      this.open = false;
      this.removeAttribute("open");
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("renders line numbers, highlights target range, and switches to diff tab", async () => {
    const scrollSpy = vi.fn();
    Element.prototype.scrollIntoView = scrollSpy;
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          kind: "text",
          path: "/repo/src/app.go",
          display_path: "src/app.go",
          root_kind: "candidate",
          content: "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}",
          start_line: 1,
          end_line: 5,
          total_lines: 5,
          highlight_start: 3,
          highlight_end: 4,
          truncated: false,
          diff: "diff --git a/src/app.go b/src/app.go\n@@ -3,2 +3,2 @@\n-\tprintln(\"old\")\n+\tprintln(\"hi\")",
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );

    render(
      <FileViewerModal
        threadID="th-1"
        path="src/app.go"
        startLine={3}
        endLine={4}
        close={() => {}}
      />,
    );

    const viewport = await screen.findByTestId("file-code-viewport");
    const line3 = viewport.querySelector('[data-line="3"]');
    const line4 = viewport.querySelector('[data-line="4"]');
    const line2 = viewport.querySelector('[data-line="2"]');
    expect(line3).toHaveAttribute("data-highlighted", "true");
    expect(line4).toHaveAttribute("data-highlighted", "true");
    expect(line2).not.toHaveAttribute("data-highlighted");
    await waitFor(() => expect(scrollSpy).toHaveBeenCalled());

    fireEvent.click(screen.getByRole("tab", { name: "变更 Diff" }));
    const diffView = screen.getByTestId("file-diff-viewport");
    expect(diffView).toHaveTextContent('+ println("hi")');
  });

  it("renders visual evidence thumbnail strip on member messages and opens lightbox", () => {
    const message: Message = {
      id: "msg-1",
      thread_id: "th-1",
      run_id: "run-1",
      step_id: "step-1",
      agent_id: "coder",
      role: "assistant",
      agent_name: "布偶猫",
      content: "已完成页面验收，截图如下。",
      status: "completed",
      created_at: "2026-11-28T00:00:00Z",
    };
    const visualSources: Source[] = [
      {
        id: "src-shot-1",
        workspace_id: "ws-1",
        thread_id: "th-1",
        run_id: "run-1",
        capture_step_id: "step-1",
        kind: "image",
        title: "acceptance_home.png",
        url: "",
        path: "captures/acceptance_home.png",
        start_line: 0,
        end_line: 0,
        sha256: "",
        content: "",
        truncated: false,
        captured_at: "2026-11-28T00:00:00Z",
        media_type: "image/png",
      },
    ];
    const opened: Array<{ src: string; title: string }> = [];
    render(
      <MessageCard
        message={message}
        threadID="th-1"
        visualSources={visualSources}
        onOpenImage={(img) => opened.push(img)}
      />,
    );

    const strip = screen.getByTestId("visual-evidence-strip");
    const img = strip.querySelector("img");
    expect(img).toHaveAttribute("src", "/api/v1/sources/src-shot-1/original?inline=1");
    fireEvent.click(img!.closest("button")!);
    expect(opened).toEqual([
      {
        src: "/api/v1/sources/src-shot-1/original?inline=1",
        title: "acceptance_home.png",
        downloadUrl: "/api/v1/sources/src-shot-1/original?inline=1",
        sourceId: "src-shot-1",
        path: "captures/acceptance_home.png",
      },
    ]);
  });
});
