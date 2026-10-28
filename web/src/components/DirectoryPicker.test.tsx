import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import type { DirectoryListing } from "../api/client";
import { DirectoryPicker } from "./DirectoryPicker";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const root: DirectoryListing = {
  path: "/public",
  parent: "",
  items: [{ name: "中文 项目", path: "/public/中文 项目" }],
  truncated: false,
};

it("navigates folders, goes back to the allowed roots and explicitly chooses a directory", async () => {
  const choose = vi.fn();
  const fetcher = vi.fn(async (url: string) => {
    const path = new URL(url, "http://local.test").searchParams.get("path");
    if (path === "")
      return Response.json({
        ...root,
        path: "",
        items: [{ name: "/public", path: "/public" }],
      });
    if (path === "/public/中文 项目")
      return Response.json({
        path,
        parent: "/public",
        items: [],
        truncated: false,
      });
    return Response.json(root);
  });
  vi.stubGlobal("fetch", fetcher);
  render(
    <DirectoryPicker
      initialPath="/public"
      restricted
      choose={choose}
      close={() => {}}
    />,
  );
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "中文 项目" }));
  await screen.findByText("没有可选的子目录");
  expect(choose).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "返回上级" }));
  await screen.findByRole("button", { name: "中文 项目" });
  await user.click(screen.getByRole("button", { name: "允许的目录" }));
  await screen.findByRole("button", { name: "/public" });
  expect(screen.getByRole("button", { name: "选择此目录" })).toBeDisabled();
  await user.click(
    screen.getByRole("button", { name: "/public" }),
  );
  await screen.findByRole("button", { name: "中文 项目" });
  await user.click(screen.getByRole("button", { name: "选择此目录" }));
  expect(choose).toHaveBeenCalledExactlyOnceWith("/public");
});

it("shows access errors, preserves the entered path and never submits the workspace on Enter", async () => {
  const submit = vi.fn((event) => event.preventDefault());
  const close = vi.fn();
  const choose = vi.fn();
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) =>
      url.includes("missing")
        ? Response.json(
            { error: { message: "目录不存在或无法访问" } },
            { status: 400 },
          )
        : Response.json(root),
    ),
  );
  render(
    <form onSubmit={submit}>
      <DirectoryPicker
        initialPath="/public"
        restricted={false}
        choose={choose}
        close={close}
      />
    </form>,
  );
  const user = userEvent.setup();
  await screen.findByRole("button", { name: "中文 项目" });
  await user.clear(screen.getByLabelText("浏览位置"));
  await user.type(screen.getByLabelText("浏览位置"), "/missing{Enter}");
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "目录不存在或无法访问",
  );
  expect(screen.getByLabelText("浏览位置")).toHaveValue("/missing");
  expect(screen.getByRole("button", { name: "选择此目录" })).toBeDisabled();
  expect(submit).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "取消选择" }));
  expect(close).toHaveBeenCalledOnce();
  expect(choose).not.toHaveBeenCalled();
});

it("ignores a stale response while navigating and describes partial results", async () => {
  let finish: (reply: Response) => void = () => {};
  const initial = new Promise<Response>((resolve) => {
    finish = resolve;
  });
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string) =>
      url.includes("path=%2Fpublic")
        ? initial
        : Promise.resolve(
            Response.json({
              ...root,
              path: "/new",
              items: [],
              truncated: true,
            }),
          ),
    ),
  );
  render(
    <DirectoryPicker
      initialPath="/public"
      restricted={false}
      choose={() => {}}
      close={() => {}}
    />,
  );
  const user = userEvent.setup();
  await user.clear(screen.getByLabelText("浏览位置"));
  await user.type(screen.getByLabelText("浏览位置"), "/new{Enter}");
  await screen.findByText(/目录条目较多/);
  await act(async () => finish(Response.json(root)));
  await waitFor(() =>
    expect(screen.getByLabelText("浏览位置")).toHaveValue("/new"),
  );
  expect(
    screen.queryByRole("button", { name: "中文 项目" }),
  ).not.toBeInTheDocument();
});
