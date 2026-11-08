import { useState, type ComponentProps } from "react";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { Composer, type WorkflowKey } from "./Composer";
import type { Agent } from "../api/client";
import type { Step } from "../api/client";
import { chooseOption } from "../test/select";

const agents: Agent[] = [
  {
    id: "one",
    workspace_id: "space",
    display_name: "布偶猫",
    handle: "ragdoll",
    aliases: [],
    role_description: "",
    executor: "claude",
    model: "default",
    tools: [],
    disabled: false,
    revision: 1,
  },
];

function Example({
  initialDraft = "",
  ...props
}: Partial<ComponentProps<typeof Composer>> & { initialDraft?: string }) {
  const [draft, setDraft] = useState(initialDraft);
  const [workflow, setWorkflow] = useState<WorkflowKey>("free");
  return (
    <Composer
      agents={agents}
      allAgents={agents}
      draft={draft}
      setDraft={setDraft}
      workflow={workflow}
      setWorkflow={setWorkflow}
      clearArtifactRef={() => {}}
      active={false}
      canceling={false}
      busy={false}
      disabled={false}
      onSend={() => {}}
      onStop={() => {}}
      {...props}
    />
  );
}
function stubViewport(initiallyCompact: boolean) {
  let matches = initiallyCompact;
  const listeners = new Set<() => void>();
  const media = {
    get matches() {
      return matches;
    },
    media: "(max-width: 900px)",
    onchange: null,
    addEventListener: (_: string, listener: () => void) => {
      listeners.add(listener);
    },
    removeEventListener: (_: string, listener: () => void) => {
      listeners.delete(listener);
    },
    addListener: (listener: () => void) => {
      listeners.add(listener);
    },
    removeListener: (listener: () => void) => {
      listeners.delete(listener);
    },
    dispatchEvent: () => true,
  };
  vi.stubGlobal("matchMedia", () => media);
  return {
    set(next: boolean) {
      matches = next;
      act(() => listeners.forEach((listener) => listener()));
    },
  };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

/** Runs animation frames on demand so deferred caret restores are observable. */
function controlFrames() {
  const frames = new Map<number, FrameRequestCallback>();
  let next = 0;
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
    next += 1;
    frames.set(next, callback);
    return next;
  });
  vi.stubGlobal("cancelAnimationFrame", (id: number) => {
    frames.delete(id);
  });
  return {
    pending: () => frames.size,
    flush() {
      const callbacks = [...frames.values()];
      frames.clear();
      act(() => callbacks.forEach((callback) => callback(performance.now())));
    },
  };
}

/** Mirrors the browser's Shift+Enter: insert a newline at the caret. */
function insertShiftEnter(input: HTMLTextAreaElement) {
  fireEvent.keyDown(input, { key: "Enter", shiftKey: true });
  const start = input.selectionStart;
  const next = `${input.value.slice(0, start)}\n${input.value.slice(input.selectionEnd)}`;
  fireEvent.change(input, { target: { value: next } });
  input.setSelectionRange(start + 1, start + 1);
  return next;
}

it("sends with Enter and inserts a newline with Shift+Enter", async () => {
  const send = vi.fn();
  render(<Example onSend={send} />);
  const user = userEvent.setup();
  const input = screen.getByLabelText("输入问题");
  await user.type(input, "第一行");
  await user.keyboard("{Shift>}{Enter}{/Shift}第二行");
  expect(input).toHaveValue("第一行\n第二行");
  expect(send).not.toHaveBeenCalled();
  await user.keyboard("{Enter}");
  expect(send).toHaveBeenCalledOnce();
  expect(input).toHaveValue("第一行\n第二行");
});

it("does not send when Enter confirms IME composition or is held down", () => {
  const send = vi.fn();
  render(<Example onSend={send} initialDraft="中文输入" />);
  const input = screen.getByLabelText("输入问题");
  fireEvent.compositionStart(input);
  fireEvent.keyDown(input, { key: "Enter" });
  fireEvent.compositionEnd(input);
  fireEvent.keyDown(input, { key: "Enter", isComposing: true });
  fireEvent.keyDown(input, { key: "Enter", keyCode: 229 });
  fireEvent.keyDown(input, { key: "Enter", repeat: true });
  expect(send).not.toHaveBeenCalled();
  fireEvent.keyDown(input, { key: "Enter" });
  expect(send).toHaveBeenCalledOnce();
});

it("offers a document attachment control", async () => {
  const attach = vi.fn();
  const user = userEvent.setup({ applyAccept: false });
  render(<Example onAttach={attach} />);
  const file = new File(["document"], "report.docx", {
    type: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  });
  await user.upload(screen.getByLabelText("添加资料"), file);
  expect(attach).toHaveBeenCalledWith(file);
});

it("shrinks the input after a long draft is cleared or replaced", () => {
  const { rerender } = render(<Example draft="长草稿" />);
  const input = screen.getByLabelText("输入问题") as HTMLTextAreaElement;
  Object.defineProperty(input, "scrollHeight", {
    get: () => (input.value ? 180 : 52),
  });
  rerender(<Example draft="替换后的长草稿" />);
  expect(input.style.height).toBe("180px");
  rerender(<Example draft="" />);
  expect(input.style.height).toBe("52px");
});

it.each([
  { name: "empty", initialDraft: "   " },
  { name: "submitting", initialDraft: "问题", busy: true },
  { name: "unavailable", initialDraft: "问题", disabled: true },
])("does not send while $name", (props) => {
  const send = vi.fn();
  render(<Example onSend={send} {...props} />);
  fireEvent.keyDown(screen.getByLabelText("输入问题"), { key: "Enter" });
  expect(send).not.toHaveBeenCalled();
});

it("changes the project execution mode before the coordinator chip", async () => {
  const change = vi.fn().mockResolvedValue(undefined);
  render(
    <Example
      executionMode="controlled"
      onExecutionModeChange={change}
      coordinator={{ name: "布偶猫", fallback: false }}
    />,
  );
  const user = userEvent.setup();
  const mode = screen.getByRole("combobox", { name: "权限模式" });
  const coordinator = screen.getByText("协调者：布偶猫");
  expect(
    mode.compareDocumentPosition(coordinator) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();
  await chooseOption(user, mode, "full_access");
  expect(change).toHaveBeenCalledWith("full_access");
});

it("saves a supplement while running and keeps stop available", async () => {
  const send = vi.fn();
  const stop = vi.fn();
  render(<Example onSend={send} onStop={stop} active initialDraft="保留原约束" />);
  const user = userEvent.setup();
  fireEvent.keyDown(screen.getByLabelText("输入问题"), { key: "Enter" });
  expect(send).toHaveBeenCalledOnce();
  expect(screen.getByLabelText("输入问题")).toHaveAttribute(
    "placeholder",
    "一起构建什么？（Enter 发送 · Shift + Enter 换行）",
  );
  await user.click(screen.getByRole("button", { name: "停止协作" }));
  expect(stop).toHaveBeenCalledOnce();
});

it("keeps the pending completion caret on the normal method pick", async () => {
  const frames = controlFrames();
  const user = userEvent.setup();
  render(<Example initialDraft="先写的一句话" />);
  const input = screen.getByLabelText("输入问题") as HTMLTextAreaElement;
  fireEvent.change(input, { target: { value: "先写的一句话\n" } });
  input.setSelectionRange(7, 7);
  await user.type(input, "/研究");
  await user.keyboard("{Enter}");
  // The command and its separator whitespace go, leaving the user's wording.
  expect(input.value).toBe("先写的一句话");
  expect(frames.pending()).toBe(1);
  frames.flush();
  expect(input).toHaveFocus();
  expect(input.selectionStart).toBe(6);
  await user.type(input, "继续");
  expect(input.value).toBe("先写的一句话继续");
});

it("keeps the pending completion caret on the normal member pick", async () => {
  const frames = controlFrames();
  const user = userEvent.setup();
  render(<Example />);
  const input = screen.getByLabelText("输入问题") as HTMLTextAreaElement;
  await user.type(input, "请 @rag");
  await user.keyboard("{Enter}");
  expect(input).toHaveValue("请 @ragdoll ");
  expect(frames.pending()).toBe(1);
  frames.flush();
  expect(input).toHaveFocus();
  expect(input.selectionStart).toBe("请 @ragdoll ".length);
});

it("does not reset the caret when a method pick frame is followed by an edit", async () => {
  const frames = controlFrames();
  const user = userEvent.setup();
  render(<Example />);
  const input = screen.getByLabelText("输入问题") as HTMLTextAreaElement;
  await user.type(input, "/研究");
  await user.keyboard("{Enter}");
  expect(input).toHaveValue("");
  expect(frames.pending()).toBe(1);
  // A later fill/edit arrives before the frame runs.
  fireEvent.change(input, {
    target: { value: "第一行\nhttps://example.org/a" },
  });
  expect(frames.pending()).toBe(0);
  frames.flush();
  expect(input).toHaveValue("第一行\nhttps://example.org/a");
  expect(input.selectionStart).toBe(input.value.length);
  // Shift+Enter therefore appends at the end, not at offset 0.
  const shifted = insertShiftEnter(input);
  expect(input.value.endsWith("\n")).toBe(true);
  expect(input).toHaveValue(shifted);
  expect(input.value.startsWith("\n")).toBe(false);
});

it("does not reset the caret when a member pick frame is followed by an edit", async () => {
  const frames = controlFrames();
  const user = userEvent.setup();
  render(<Example />);
  const input = screen.getByLabelText("输入问题") as HTMLTextAreaElement;
  await user.type(input, "请 @rag");
  await user.keyboard("{Enter}");
  expect(frames.pending()).toBe(1);
  fireEvent.change(input, { target: { value: "新的多行草稿" } });
  input.setSelectionRange(6, 6);
  frames.flush();
  expect(input).toHaveValue("新的多行草稿");
  expect(input.selectionStart).toBe(6);
  expect(input).not.toHaveValue("请 @ragdoll ");
});

it("skips a stale completion frame when the draft changes outside an edit", () => {
  const frames = controlFrames();
  const { rerender } = render(<Example />);
  const input = screen.getByLabelText("输入问题") as HTMLTextAreaElement;
  fireEvent.change(input, { target: { value: "/研究" } });
  fireEvent.keyDown(input, { key: "Enter" });
  expect(frames.pending()).toBe(1);
  // The host replaced the draft (restored input) without a DOM input event.
  rerender(<Example draft="宿主恢复的草稿" />);
  input.setSelectionRange(3, 3);
  frames.flush();
  expect(input).toHaveValue("宿主恢复的草稿");
  expect(input.selectionStart).toBe(3);
});

it("supersedes an older completion frame with a newer selection", async () => {
  const frames = controlFrames();
  const user = userEvent.setup();
  render(<Example />);
  const input = screen.getByLabelText("输入问题") as HTMLTextAreaElement;
  await user.type(input, "/研究");
  await user.keyboard("{Enter}");
  expect(frames.pending()).toBe(1);
  fireEvent.change(input, { target: { value: "a" } });
  await user.type(input, "b");
  input.setSelectionRange(0, 0);
  fireEvent.select(input);
  frames.flush();
  expect(input).toHaveValue("ab");
  expect(input.selectionStart).toBe(0);
});

it("does not steal focus back from another control after a completion", async () => {
  const frames = controlFrames();
  const user = userEvent.setup();
  render(<Example />);
  const input = screen.getByLabelText("输入问题") as HTMLTextAreaElement;
  await user.type(input, "/研究");
  await user.keyboard("{Enter}");
  expect(frames.pending()).toBe(1);
  const other = screen.getByRole("button", { name: "清除本次协作模式" });
  other.focus();
  frames.flush();
  expect(other).toHaveFocus();
  expect(input.selectionStart).toBe(0);
});

it("completes an @ mention before sending and lets Shift+Enter keep a newline", async () => {
  const send = vi.fn();
  render(<Example onSend={send} />);
  const user = userEvent.setup();
  const input = screen.getByLabelText("输入问题");
  expect(screen.queryByLabelText(/^成员与分工/)).not.toBeInTheDocument();
  expect(screen.queryByText("自动指派")).not.toBeInTheDocument();
  await user.type(input, "请 @rag");
  expect(screen.getByRole("listbox", { name: "选择成员" })).toBeVisible();
  await user.keyboard("{Enter}");
  expect(input).toHaveValue("请 @ragdoll ");
  expect(send).not.toHaveBeenCalled();
  await user.keyboard("{Enter}");
  expect(send).toHaveBeenCalledOnce();
  await user.clear(input);
  await user.type(input, "@rag");
  await user.keyboard("{Shift>}{Enter}{/Shift}");
  expect(input).toHaveValue("@rag\n");
  expect(send).toHaveBeenCalledOnce();
});

it("keeps the slash command and mention affordances without a session assignment editor", async () => {
  const send = vi.fn();
  render(<Example onSend={send} />);
  const user = userEvent.setup();
  expect(
    screen.queryByText("资料", { selector: "summary" }),
  ).not.toBeInTheDocument();
  expect(screen.queryByLabelText("资料链接")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("研究范围")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("工作方式")).not.toBeInTheDocument();
  const input = screen.getByLabelText("输入问题");
  await user.type(input, "/研究");
  const listbox = screen.getByRole("listbox", { name: "选择协作模式" });
  expect(listbox).toBeVisible();
  expect(screen.queryByLabelText(/^成员与分工/)).not.toBeInTheDocument();
  expect(screen.queryByLabelText("研究")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("质疑")).not.toBeInTheDocument();
  await user.keyboard("{Enter}");
  expect(screen.getByText("研究协作")).toBeVisible();
  expect(input).toHaveValue("");
  await user.type(input, "研究 https://example.org/");
  await user.keyboard("{Enter}");
  expect(send).toHaveBeenCalledOnce();
  expect(screen.getByText("研究协作")).toBeVisible();
});

it("lets a slash command pick the method and clears it back to coordinator choice", async () => {
  render(<Example />);
  const user = userEvent.setup();
  const input = screen.getByLabelText("输入问题");
  await user.type(input, "/");
  const options = screen.getAllByRole("option");
  expect(options.map((option) => option.textContent)).toEqual([
    "💬自由协作",
    "👥协作讨论",
    "🔬研究协作",
    "🛠️工程协作",
  ]);
  await user.type(input, "工程");
  await user.keyboard("{Enter}");
  expect(screen.getByText("工程协作")).toBeVisible();
  expect(
    screen.getByText(/按成员已获授权的能力执行/),
  ).toBeVisible();
  expect(input).toHaveValue("");
  await user.click(screen.getByRole("button", { name: "清除本次协作模式" }));
  expect(screen.queryByText("工程协作")).not.toBeInTheDocument();
  expect(
    screen.queryByText(/按成员已获授权的能力执行/),
  ).not.toBeInTheDocument();
});

it("shows the actual coordinator and the accepted routing summary", () => {
  const routing = {
    assignments: {
      researcher: "one",
      critic: "one",
      coordinator: "one",
    },
    clarification: "",
    decision: "assign",
    evidence: [],
    explanation: "",
    input_tokens: 0,
    model_call: false,
    output_tokens: 0,
    targets: [],
    usage_known: false,
    workflow: "research",
  };
  const { rerender } = render(
    <Example
      coordinator={{ name: "布偶猫", fallback: false }}
      routing={routing}
    />,
  );
  expect(screen.getByText("协调者：布偶猫")).toBeVisible();
  expect(
    screen.getByText("研究：布偶猫 · 质疑：布偶猫 · 汇总：布偶猫"),
  ).toBeVisible();
  rerender(<Example coordinator={{ name: "缅因猫", fallback: true }} />);
  expect(screen.getByText("默认协调者不可用，使用缅因猫")).toBeVisible();
});

it("shows a dismissible clarification above the input without changing the draft", async () => {
  const routing = {
    decision: "clarify",
    workflow: "free",
    targets: [],
    assignments: {},
    explanation: "",
    clarification: "成员不可用，请选择有效成员。",
    evidence: [],
    model_call: false,
    usage_known: false,
    input_tokens: 0,
    output_tokens: 0,
  };
  const send = vi.fn();
  const { rerender } = render(
    <Example initialDraft="@missing 比较两份文档" routing={routing} onSend={send} />,
  );
  const input = screen.getByLabelText("输入问题");
  const notice = screen.getByText(routing.clarification).closest('[role="status"]')!;
  expect(input.closest("form")).toContainElement(notice as HTMLElement);
  expect(notice.compareDocumentPosition(input) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  await userEvent.setup().click(screen.getByRole("button", { name: "关闭提示" }));
  expect(screen.queryByText(routing.clarification)).not.toBeInTheDocument();
  expect(input).toHaveValue("@missing 比较两份文档");
  expect(input).toHaveFocus();
  expect(send).not.toHaveBeenCalled();
  rerender(<Example routing={routing} />);
  expect(screen.queryByText(routing.clarification)).not.toBeInTheDocument();
  rerender(<Example routing={{ ...routing }} />);
  expect(screen.getByText(routing.clarification)).toBeVisible();
});

it("keeps submission errors beside the input and dismisses them without sending", async () => {
  const dismiss = vi.fn();
  const send = vi.fn();
  render(<Example error="暂时无法提交，请重试。" onDismissError={dismiss} onSend={send} initialDraft="保留的草稿" />);
  const notice = screen.getByRole("alert");
  const input = screen.getByLabelText("输入问题");
  expect(input.closest("form")).toContainElement(notice);
  expect(notice).toHaveTextContent("暂时无法提交，请重试。");
  await userEvent.setup().click(screen.getByRole("button", { name: "关闭错误提示" }));
  expect(dismiss).toHaveBeenCalledOnce();
  expect(input).toHaveValue("保留的草稿");
  expect(input).toHaveFocus();
  expect(send).not.toHaveBeenCalled();
});

it("offers a member-specific retry above the input and preserves a draft when dismissed", async () => {
  const retry = vi.fn();
  const failed = { id: "failed", agent_name: "橘猫", status: "failed", error: "unknown: native CLI did not confirm success (result=result_error, category=execution)" } as Step;
  const { rerender } = render(<Example failureNoticeID="run" failedSteps={[failed]} onRetryMember={retry} initialDraft="另一个问题的草稿" />);
  const button = screen.getByRole("button", { name: "重试 橘猫" });
  const input = screen.getByLabelText("输入问题");
  expect(input.closest("form")).toContainElement(button);
  expect(screen.getByText(/重试只补跑该成员并沿用原流程/)).toBeVisible();
  expect(screen.getByText(/Gemini 未确认回复成功/)).toBeVisible();
  await userEvent.setup().click(button);
  expect(retry).toHaveBeenCalledWith("failed");
  expect(input).toHaveValue("另一个问题的草稿");
  await userEvent.setup().click(screen.getByRole("button", { name: "关闭提示" }));
  expect(screen.queryByRole("button", { name: "重试 橘猫" })).not.toBeInTheDocument();
  expect(input).toHaveFocus();
  rerender(<Example failureNoticeID="next-failed-run" failedSteps={[failed]} />);
  expect(screen.getByRole("button", { name: "重试 橘猫" })).toBeVisible();
});

it("lets users dismiss a failed execution-mode change and try again", async () => {
  const change = vi.fn().mockRejectedValue(new Error("权限模式保存失败"));
  render(<Example executionMode="controlled" onExecutionModeChange={change} />);
  const user = userEvent.setup();
  await chooseOption(user, screen.getByRole("combobox", { name: "权限模式" }), "full_access");
  expect(await screen.findByRole("alert")).toHaveTextContent("权限模式保存失败");
  await user.click(screen.getByRole("button", { name: "关闭提示" }));
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  await chooseOption(user, screen.getByRole("combobox", { name: "权限模式" }), "full_access");
  expect(await screen.findByRole("alert")).toHaveTextContent("权限模式保存失败");
});

it("renders only an accessible stop icon while running", async () => {
  const stop = vi.fn();
  const { rerender } = render(<Example active onStop={stop} />);
  const user = userEvent.setup();
  const control = screen.getByRole("button", { name: "停止协作" });
  expect(control.textContent).toBe("");
  expect(control.querySelector("svg")).toBeInTheDocument();
  await user.click(control);
  expect(stop).toHaveBeenCalledOnce();
  rerender(<Example active canceling onStop={stop} />);
  expect(screen.getByRole("button", { name: "正在停止" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "正在停止" }).textContent).toBe("");
});

it("keeps engineering as one workflow without an analysis/development selector", async () => {
  render(<Example />);
  const user = userEvent.setup();
  const input = screen.getByLabelText("输入问题");
  await user.type(input, "/工程");
  await user.keyboard("{Enter}");
  expect(screen.queryByLabelText("工程动作")).not.toBeInTheDocument();
  expect(
    screen.getByText(/按成员已获授权的能力执行/),
  ).toBeVisible();
  // Assignment controls stay out of the composer.
  expect(screen.queryByText("分工")).not.toBeInTheDocument();
});

it("uses a filled square stop control in the send theme without changing cancel behavior", async () => {
  const stop = vi.fn();
  render(<Example active draft="正在执行" onStop={stop} />);
  const user = userEvent.setup();
  const button = screen.getByRole("button", { name: "停止协作" });
  expect(button.className).toMatch(/_stop_/);
  expect(button.className).toMatch(/_composer-send_/);
  const square = button.querySelector("rect");
  expect(square).toHaveAttribute("fill", "currentColor");
  await user.click(button);
  expect(stop).toHaveBeenCalledOnce();
});

it("shows the exact desktop placeholder and keeps @ and / guidance out of it", () => {
  render(<Example />);
  const input = screen.getByLabelText("输入问题");
  expect(input).toHaveAttribute(
    "placeholder",
    "一起构建什么？（Enter 发送 · Shift + Enter 换行）",
  );
  expect(input.getAttribute("placeholder")).not.toMatch(/[@/]/);
  expect(
    screen.getByRole("button", { name: "输入提示：/ 选择协作模式，@ 指定成员" }),
  ).toBeVisible();
});

it("switches the exact placeholder across the 900px breakpoint while resizing", async () => {
  const viewport = stubViewport(false);
  render(<Example />);
  const input = screen.getByLabelText("输入问题");
  expect(input).toHaveAttribute(
    "placeholder",
    "一起构建什么？（Enter 发送 · Shift + Enter 换行）",
  );
  viewport.set(true);
  await waitFor(() =>
    expect(input).toHaveAttribute("placeholder", "一起构建什么？"),
  );
  viewport.set(false);
  await waitFor(() =>
    expect(input).toHaveAttribute(
      "placeholder",
      "一起构建什么？（Enter 发送 · Shift + Enter 换行）",
    ),
  );
});

it("uses the compact placeholder when the narrow breakpoint starts active", () => {
  stubViewport(true);
  render(<Example />);
  expect(screen.getByLabelText("输入问题")).toHaveAttribute(
    "placeholder",
    "一起构建什么？",
  );
});

it("opens on mouse hover, closes when the pointer leaves, and keeps the draft", async () => {
  render(<Example initialDraft="保留草稿" />);
  const user = userEvent.setup();
  const hint = screen.getByRole("button", {
    name: "输入提示：/ 选择协作模式，@ 指定成员",
  });
  const input = screen.getByLabelText("输入问题");
  expect(hint).not.toHaveAttribute("title");
  await user.hover(hint);
  const tooltip = await screen.findByRole("tooltip");
  expect(tooltip).toHaveTextContent("@ 指定成员");
  expect(tooltip).toHaveTextContent("/ 选择协作模式");
  expect(hint).toHaveAttribute("aria-describedby", tooltip.id);
  expect(input.closest("form")).not.toContainElement(tooltip);
  expect(tooltip.parentElement).toBe(document.body);
  await user.unhover(hint);
  await waitFor(() =>
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument(),
  );
  expect(input).toHaveValue("保留草稿");
});

it("opens on keyboard focus and closes on blur or Escape", async () => {
  render(<Example initialDraft="保留草稿" />);
  const user = userEvent.setup();
  const hint = screen.getByRole("button", {
    name: "输入提示：/ 选择协作模式，@ 指定成员",
  });
  const input = screen.getByLabelText("输入问题");
  await user.tab();
  expect(hint).toHaveFocus();
  expect(await screen.findByRole("tooltip")).toBeVisible();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  await user.keyboard("{Enter}");
  expect(await screen.findByRole("tooltip")).toBeVisible();
  await user.tab();
  await waitFor(() =>
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument(),
  );
  expect(input).toHaveValue("保留草稿");
});

it("opens on the first click and closes on the second without submitting or clearing the draft", async () => {
  const send = vi.fn();
  render(<Example initialDraft="草稿" onSend={send} />);
  const user = userEvent.setup();
  const hint = screen.getByRole("button", {
    name: "输入提示：/ 选择协作模式，@ 指定成员",
  });
  await user.click(hint);
  expect(await screen.findByRole("tooltip")).toBeVisible();
  await user.click(hint);
  await waitFor(() =>
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument(),
  );
  expect(send).not.toHaveBeenCalled();
  expect(screen.getByLabelText("输入问题")).toHaveValue("草稿");
});

it("opens and closes the tooltip with a full touch tap sequence", async () => {
  const send = vi.fn();
  render(<Example initialDraft="草稿" onSend={send} />);
  const user = userEvent.setup();
  const hint = screen.getByRole("button", {
    name: "输入提示：/ 选择协作模式，@ 指定成员",
  });
  await user.pointer({ keys: "[TouchA]", target: hint });
  expect(await screen.findByRole("tooltip")).toBeVisible();
  await user.pointer({ keys: "[TouchA]", target: hint });
  await waitFor(() =>
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument(),
  );
  expect(send).not.toHaveBeenCalled();
  expect(screen.getByLabelText("输入问题")).toHaveValue("草稿");
});

it("flips and clamps the tooltip so the composer edge cannot clip it", async () => {
  vi.spyOn(window, "innerHeight", "get").mockReturnValue(800);
  render(<Example />);
  const hint = screen.getByRole("button", {
    name: "输入提示：/ 选择协作模式，@ 指定成员",
  });
  const rect = (top: number, bottom: number, left: number, right: number) =>
    ({
      top,
      bottom,
      left,
      right,
      width: right - left,
      height: bottom - top,
      x: left,
      y: top,
      toJSON: () => ({}),
    }) as DOMRect;
  const place = vi
    .spyOn(hint, "getBoundingClientRect")
    .mockReturnValue(rect(8, 32, 600, 624));
  fireEvent.pointerEnter(hint, { pointerType: "mouse" });
  const tooltip = await screen.findByRole("tooltip");
  await waitFor(() => expect(tooltip).toHaveAttribute("data-placement", "bottom"));
  expect(parseFloat(tooltip.style.top)).toBeGreaterThan(32);
  fireEvent.pointerLeave(hint, { pointerType: "mouse" });
  place.mockReturnValue(rect(740, 764, -20, 4));
  fireEvent.pointerEnter(hint, { pointerType: "mouse" });
  const flipped = await screen.findByRole("tooltip");
  await waitFor(() => expect(flipped).toHaveAttribute("data-placement", "top"));
  expect(parseFloat(flipped.style.top)).toBeLessThan(740);
  expect(parseFloat(flipped.style.left)).toBeGreaterThanOrEqual(8);
});

it("keeps the method chip icon and close target small but tappable", async () => {
  render(<Example />);
  const user = userEvent.setup();
  const input = screen.getByLabelText("输入问题");
  await user.type(input, "/研究");
  await user.keyboard("{Enter}");
  const chip = screen.getByText("研究协作").closest('[role="status"]') as HTMLElement;
  const [semantic, close] = chip.querySelectorAll("svg");
  expect(semantic).toHaveAttribute("width", "13");
  expect(close).toHaveAttribute("width", "12");
  const clear = within(chip).getByRole("button", { name: "清除本次协作模式" });
  expect(clear.className).toMatch(/_method-chip-clear_/);
  await user.click(clear);
  expect(screen.queryByText("研究协作")).not.toBeInTheDocument();
});

it("shows only the permission select value without a visible 权限 prefix", () => {
  render(
    <Example
      executionMode="controlled"
      onExecutionModeChange={() => {}}
    />,
  );
  const mode = screen.getByRole("combobox", { name: "权限模式" });
  expect(mode).toHaveTextContent("受控");
  expect(mode.querySelector("svg")).toBeInTheDocument();
  expect(screen.queryByText("权限")).not.toBeInTheDocument();
});
