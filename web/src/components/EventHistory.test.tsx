import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { EventHistory } from "./EventHistory";
import type { Agent, Event } from "../api/client";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const agent: Agent = {
  id: "member",
  workspace_id: "ws",
  display_name: "研究成员",
  handle: "member",
  aliases: [],
  role_description: "",
  model: "default",
  tools: [],
  disabled: false,
  revision: 1,
};

const start = (seq: number, name: string): Event => ({
  schema_version: 1,
  run_id: "run",
  seq,
  type: "tool_started",
  occurred_at: "",
  agent_id: "member",
  step_id: "step",
  data: { tool_call_id: `call-${seq}`, name, input: {} },
});

it("remounts for a new start sequence so an old page cannot win", async () => {
  let releaseOld: (value: Response) => void = () => {};
  const oldPage = new Promise<Response>((resolve) => {
    releaseOld = resolve;
  });
  const fetcher = vi.fn((path: string) => {
    if (path.endsWith("after=10")) return oldPage;
    return Promise.resolve(
      Response.json({ items: [start(100, "fetch_url")], next_cursor: "" }),
    );
  });
  vi.stubGlobal("fetch", fetcher);
  const props = {
    runID: "run",
    agents: [agent],
    sources: [],
    close: () => {},
  };
  const { rerender } = render(<EventHistory {...props} after={10} />);
  rerender(<EventHistory {...props} after={100} />);
  expect(await screen.findByText("读取网页")).toBeVisible();
  expect(fetcher.mock.calls[0][0]).toContain("after=10");
  expect(fetcher.mock.calls.at(-1)?.[0]).toContain("after=100");

  await act(async () => {
    releaseOld(
      Response.json({ items: [start(10, "read_file")], next_cursor: "" }),
    );
    await oldPage;
  });
  expect(screen.queryByText("读取文件")).not.toBeInTheDocument();
  expect(screen.getByText("读取网页")).toBeVisible();
});

it("bounds cursors past 64 pages and returns to this history's start", async () => {
  const fetcher = vi.fn((path: string) => {
    const after = new URL(path, "http://local").searchParams.get("after") ?? "";
    const page = !after || after === "500" ? 0 : Number(after.slice(7));
    return Promise.resolve(
      Response.json({
        items: [start(700 + page, `call-${page}`)],
        next_cursor: `cursor-${page + 1}`,
      }),
    );
  });
  vi.stubGlobal("fetch", fetcher);
  const user = userEvent.setup();
  const click = async (name: string) => {
    const button = screen.getByRole("button", { name });
    await waitFor(() => expect(button).toBeEnabled());
    await user.click(button);
  };
  render(
    <EventHistory
      runID="run"
      after={500}
      agents={[agent]}
      sources={[]}
      close={() => {}}
    />,
  );

  expect(await screen.findByText("call-0")).toBeVisible();
  expect(fetcher.mock.calls[0][0]).toContain("after=500");
  expect(screen.getByText("第 1 页")).toBeVisible();

  for (let page = 1; page <= 70; page++) {
    await click("查看下一页历史事件");
    expect(await screen.findByText(`call-${page}`)).toBeVisible();
  }
  // The body is still one page, and the page number is absolute.
  expect(screen.queryByText("call-0")).not.toBeInTheDocument();
  expect(screen.getByText("第 71 页")).toBeVisible();

  // Pages 7..70 remain; walking back stops at the retained window edge.
  for (let page = 69; page >= 7; page--) {
    await click("查看上一页历史事件");
    expect(await screen.findByText(`call-${page}`)).toBeVisible();
  }
  expect(
    screen.getByRole("button", { name: "查看上一页历史事件" }),
  ).toBeDisabled();
  await click("回到历史事件第一页");
  expect(await screen.findByText("call-0")).toBeVisible();
  expect(screen.getByText("第 1 页")).toBeVisible();
  expect(fetcher.mock.calls.at(-1)?.[0]).toContain("after=500");
  expect(
    screen.queryByRole("button", { name: "回到历史事件第一页" }),
  ).not.toBeInTheDocument();
});
