import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { AuthScreen } from "./AuthScreen";
import { App } from "../app/App";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("confirms the first password before setting it and clears it after success", async () => {
  const fetch = vi.fn(async (path: string) =>
    Response.json(
      path === "/auth/status"
        ? { initialized: false }
        : { csrf_token: "session" },
    ),
  );
  vi.stubGlobal("fetch", fetch);
  const connect = vi.fn(async () => {});
  render(<AuthScreen required error="" connect={connect} />);
  const user = userEvent.setup();
  const password = await screen.findByLabelText("设置密码", { exact: true });
  await user.type(password, "a safe test passphrase");
  await user.type(screen.getByLabelText("确认密码"), "another test passphrase");
  await user.click(screen.getByRole("button", { name: "设置密码并进入" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("不一致");
  expect(fetch).toHaveBeenCalledTimes(1);
  await user.clear(screen.getByLabelText("确认密码"));
  await user.type(screen.getByLabelText("确认密码"), "a safe test passphrase");
  await user.click(screen.getByRole("button", { name: "设置密码并进入" }));
  await waitFor(() => expect(connect).toHaveBeenCalledOnce());
  expect(fetch).toHaveBeenCalledWith(
    "/auth/setup",
    expect.objectContaining({
      method: "POST",
      body: JSON.stringify({ password: "a safe test passphrase" }),
    }),
  );
  expect(password).toHaveValue("");
  expect(screen.getByLabelText("确认密码")).toHaveValue("");
});

it("uses the saved password, reports errors and allows a retry", async () => {
  let attempts = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      if (path === "/auth/status") return Response.json({ initialized: true });
      return ++attempts === 1
        ? Response.json({ error: { message: "密码不正确" } }, { status: 401 })
        : Response.json({ csrf_token: "session" });
    }),
  );
  const connect = vi.fn(async () => {});
  render(<AuthScreen required error="" connect={connect} />);
  const user = userEvent.setup();
  await user.type(
    await screen.findByLabelText("密码", { exact: true }),
    "a test password",
  );
  expect(screen.queryByLabelText("确认密码")).not.toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "解锁工作空间" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("密码不正确");
  expect(connect).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "解锁工作空间" }));
  await waitFor(() => expect(connect).toHaveBeenCalledOnce());
});

it("shows the password gate when the workspace session is missing", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) =>
      path === "/auth/status"
        ? Response.json({ initialized: true })
        : Response.json({ error: { message: "请输入密码" } }, { status: 401 }),
    ),
  );
  render(<App />);
  expect(await screen.findByLabelText("密码", { exact: true })).toBeVisible();
  expect(screen.queryByText("启动令牌")).not.toBeInTheDocument();
});
