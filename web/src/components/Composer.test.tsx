import { useState, type ComponentProps } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { Composer, type WorkflowKey } from "./Composer";
import type { Agent } from "../api/client";
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
      selected={[]}
      toggleSelected={() => {}}
      assignments={{ coordinator: "one" }}
      setAssignment={() => {}}
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
afterEach(cleanup);

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
  { name: "running", initialDraft: "问题", active: true },
  { name: "unavailable", initialDraft: "问题", disabled: true },
])("does not send while $name", (props) => {
  const send = vi.fn();
  render(<Example onSend={send} {...props} />);
  fireEvent.keyDown(screen.getByLabelText("输入问题"), { key: "Enter" });
  expect(send).not.toHaveBeenCalled();
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

it("keeps workflow roles in a dismissible panel and accepts links in the message", async () => {
  const send = vi.fn();
  render(<Example onSend={send} />);
  const user = userEvent.setup();
  expect(
    screen.queryByText("资料", { selector: "summary" }),
  ).not.toBeInTheDocument();
  expect(screen.queryByLabelText("资料链接")).not.toBeInTheDocument();
  expect(screen.queryByLabelText("研究范围")).not.toBeInTheDocument();
  await chooseOption(user, screen.getByLabelText("工作方式"), "research");
  const roles = screen.getByLabelText(/^成员与分工/);
  await user.click(roles);
  expect(screen.getByLabelText("研究")).toBeVisible();
  await user.keyboard("{Escape}");
  expect(roles.closest("details")).not.toHaveAttribute("open");
  expect(roles).toHaveFocus();
  const input = screen.getByLabelText("输入问题");
  await user.type(input, "研究 https://example.org/");
  await user.keyboard("{Enter}");
  expect(send).toHaveBeenCalledOnce();
  expect(input).toHaveValue("研究 https://example.org/");
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
