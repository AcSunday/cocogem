import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { MessageCard, rememberCursor } from "./MessageCard";
import type { Message, MessageContentPage } from "../api/client";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const message = (extra: Partial<Message> = {}): Message => ({
  id: "message",
  thread_id: "thread",
  run_id: "run",
  step_id: "step",
  agent_id: "agent",
  agent_name: "研究成员",
  role: "assistant",
  content: "消息预览",
  content_format: "chunks_v1",
  content_bytes: 90000,
  content_truncated: true,
  status: "completed",
  created_at: "2026-10-20T00:00:00Z",
  ...extra,
});

const page = (extra: Partial<MessageContentPage> = {}): MessageContentPage => ({
  message_id: "message",
  content_format: "chunks_v1",
  content: "第一页正文",
  content_bytes: 90000,
  content_truncated: true,
  has_more: true,
  next_cursor: "cursor-2",
  ...extra,
});

it("shows a short message without full-text controls", () => {
  render(
    <MessageCard
      message={message({
        content: "短回复",
        content_truncated: false,
        content_bytes: 9,
      })}
      index={0}
    />,
  );
  expect(screen.getByText("短回复")).toBeVisible();
  expect(
    screen.queryByRole("button", { name: "展开全文" }),
  ).not.toBeInTheDocument();
});

it("keeps member identity colors when names or roster positions change", () => {
  const { container, rerender } = render(
    <MessageCard message={message()} index={0} />,
  );
  const avatar = container.querySelector('[aria-hidden="true"]');
  const tone = avatar?.className;
  rerender(
    <MessageCard message={message({ agent_name: "新的称呼" })} index={3} />,
  );
  expect(container.querySelector('[aria-hidden="true"]')?.className).toBe(tone);
  expect(screen.getByText("新的称呼")).toBeVisible();
});

it("shows the model recorded by the message's own attempt", () => {
  const { rerender } = render(
    <MessageCard
      message={message({ model: "requested", resolved_model: "resolved" })}
      index={0}
    />,
  );
  expect(screen.getByText("研究成员")).toBeVisible();
  expect(screen.getByText("（resolved）")).toBeVisible();
  rerender(
    <MessageCard
      message={message({ model: "legacy-requested", resolved_model: undefined })}
      index={0}
    />,
  );
  expect(screen.getByText("（legacy-requested）")).toBeVisible();
});

it("labels default and unresolved models without claiming a resolved model", () => {
  const { rerender } = render(
    <MessageCard
      message={message({ model: "default", resolved_model: "cli-default" })}
      index={0}
    />,
  );
  expect(screen.getByText("（CLI 默认模型）")).toBeVisible();
  rerender(
    <MessageCard
      message={message({ model: "default", resolved_model: undefined })}
      index={0}
    />,
  );
  expect(screen.getByText("（CLI 默认模型）")).toBeVisible();
});

it("never shows a model for the user's own message", () => {
  render(
    <MessageCard
      message={message({
        role: "user",
        agent_name: "你",
        model: "default",
        resolved_model: "resolved",
      })}
      index={0}
    />,
  );
  expect(screen.getByText("你")).toBeVisible();
  expect(screen.queryByText("（resolved）")).not.toBeInTheDocument();
  expect(screen.queryByText("（CLI 默认模型）")).not.toBeInTheDocument();
});

it("replaces the current page while walking the full text, then keeps its state across rerenders", async () => {
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce(Response.json(page()))
    .mockResolvedValueOnce(
      Response.json(
        page({
          content: "第二页正文",
          has_more: false,
          next_cursor: undefined,
        }),
      ),
    )
    .mockResolvedValueOnce(Response.json(page()));
  vi.stubGlobal("fetch", fetcher);
  const user = userEvent.setup();
  const { rerender } = render(<MessageCard message={message()} index={0} />);
  expect(screen.getByText("消息预览")).toBeVisible();
  expect(screen.getByText("全文 90000 字节 · 当前为部分预览")).toBeVisible();
  expect(document.body.textContent).not.toContain("chunks_v1");
  await user.click(screen.getByRole("button", { name: "展开全文" }));
  expect(await screen.findByText("第一页正文")).toBeVisible();
  expect(screen.queryByText("消息预览")).not.toBeInTheDocument();
  expect(fetcher.mock.calls[0][0]).toBe(
    "/api/v1/runs/run/messages/message/content?limit=8",
  );
  await user.click(screen.getByRole("button", { name: "查看下一页全文" }));
  expect(await screen.findByText("第二页正文")).toBeVisible();
  expect(screen.queryByText("第一页正文")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "查看下一页全文" })).toBeDisabled();
  expect(fetcher.mock.calls[1][0]).toContain("cursor=cursor-2");

  rerender(
    <MessageCard message={message({ content: "更新后的预览" })} index={0} />,
  );
  expect(screen.getByText("第二页正文")).toBeVisible();
  expect(fetcher).toHaveBeenCalledTimes(2);

  await user.click(screen.getByRole("button", { name: "查看上一页全文" }));
  expect(await screen.findByText("第一页正文")).toBeVisible();
  expect(screen.queryByText("第二页正文")).not.toBeInTheDocument();
});

it("shows loading and retryable errors without exposing protocol fields", async () => {
  const fetcher = vi
    .fn()
    .mockRejectedValueOnce(new Error("读取失败"))
    .mockResolvedValueOnce(Response.json(page({ has_more: false })));
  vi.stubGlobal("fetch", fetcher);
  const user = userEvent.setup();
  render(<MessageCard message={message()} index={0} />);
  await user.click(screen.getByRole("button", { name: "展开全文" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("读取失败");
  expect(document.body.textContent).not.toContain("chunks_v1");
  await user.click(screen.getByRole("button", { name: "重新加载全文" }));
  expect(await screen.findByText("第一页正文")).toBeVisible();
});

it("ignores a page that resolves after the card switched to another message", async () => {
  let releaseOld: (value: Response) => void = () => {};
  const oldPage = new Promise<Response>((resolve) => {
    releaseOld = resolve;
  });
  const fetcher = vi.fn((path: string) => {
    if (path.includes("/messages/old-message/")) return oldPage;
    return Promise.resolve(
      Response.json(page({ message_id: "new-message", content: "新消息正文" })),
    );
  });
  vi.stubGlobal("fetch", fetcher);
  const user = userEvent.setup();
  const { rerender } = render(
    <MessageCard
      message={message({ id: "old-message", content: "旧消息" })}
      index={0}
    />,
  );
  await user.click(screen.getByRole("button", { name: "展开全文" }));
  rerender(
    <MessageCard
      message={message({
        id: "new-message",
        run_id: "run-2",
        content: "新消息预览",
      })}
      index={0}
    />,
  );
  expect(screen.getByText("新消息预览")).toBeVisible();
  expect(screen.queryByRole("button", { name: "收起全文" })).toBeNull();
  await act(async () => {
    releaseOld(
      Response.json(page({ message_id: "old-message", content: "旧消息正文" })),
    );
    await oldPage;
  });
  await waitFor(() =>
    expect(screen.queryByText("旧消息正文")).not.toBeInTheDocument(),
  );
  expect(screen.queryByText("旧消息正文")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "展开全文" }));
  expect(await screen.findByText("新消息正文")).toBeVisible();
  expect(fetcher).toHaveBeenCalledTimes(2);
});

it("offers the newest snapshot after appends without moving the reader there", async () => {
  const fetcher = vi
    .fn()
    .mockResolvedValueOnce(Response.json(page()))
    .mockResolvedValueOnce(
      Response.json(
        page({
          content: "第二页正文",
          has_more: false,
          next_cursor: undefined,
        }),
      ),
    )
    .mockResolvedValueOnce(
      Response.json(
        page({
          content: "最新快照首页",
          content_bytes: 120000,
          has_more: false,
        }),
      ),
    );
  vi.stubGlobal("fetch", fetcher);
  const user = userEvent.setup();
  const { rerender } = render(<MessageCard message={message()} index={0} />);
  await user.click(screen.getByRole("button", { name: "展开全文" }));
  await screen.findByText("第一页正文");
  await user.click(screen.getByRole("button", { name: "查看下一页全文" }));
  await screen.findByText("第二页正文");

  // The same message receives appended text; the open page stays put.
  rerender(
    <MessageCard
      message={message({ content_bytes: 120000, status: "completed" })}
      index={0}
    />,
  );
  expect(screen.getByText("第二页正文")).toBeVisible();
  expect(fetcher).toHaveBeenCalledTimes(2);
  await user.click(screen.getByRole("button", { name: "查看最新全文" }));
  expect(await screen.findByText("最新快照首页")).toBeVisible();
  expect(fetcher.mock.calls[2][0]).toBe(
    "/api/v1/runs/run/messages/message/content?limit=8",
  );
  expect(screen.getByText("第 1 页")).toBeVisible();
  expect(screen.queryByText("第二页正文")).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "查看最新全文" }),
  ).not.toBeInTheDocument();
});

it("bounds the retained cursor window while keeping absolute pages", () => {
  let stack = rememberCursor({ base: 0, items: [""] }, 0, "");
  for (let page = 1; page <= 100; page++)
    stack = rememberCursor(stack, page, `cursor-${page}`);
  expect(stack.items.length).toBeLessThanOrEqual(64);
  expect(stack.items.at(-1)).toBe("cursor-100");
  expect(stack.base).toBe(100 - stack.items.length + 1);
  // Jumping before the retained window resets it to that absolute page.
  expect(rememberCursor(stack, 10, "cursor-10")).toEqual({
    base: 10,
    items: ["cursor-10"],
  });
});

it("separates an empty reply still generating from one that ended empty", () => {
  const empty = { content: "", content_truncated: false, content_bytes: 0 };
  const { rerender } = render(
    <MessageCard
      message={message({ ...empty, status: "processing" })}
      index={0}
    />,
  );
  expect(screen.getByText("正在准备回复…")).toBeVisible();
  for (const status of ["interrupted", "canceled", "failed", "completed"]) {
    rerender(<MessageCard message={message({ ...empty, status })} index={0} />);
    expect(screen.queryByText("正在准备回复…")).not.toBeInTheDocument();
    expect(screen.getByText("本次回复没有正文。")).toBeVisible();
  }
});

it("marks content as still generating instead of presenting the preview as complete", () => {
  render(
    <MessageCard
      message={message({ status: "processing", content_bytes: 70000 })}
      index={0}
    />,
  );
  expect(screen.getByText("生成中")).toBeVisible();
  expect(screen.getByText("正在生成 · 已生成 70000 字节")).toBeVisible();
});
