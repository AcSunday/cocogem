import { cleanup, renderHook } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { APIError } from "../api/client";
import { errorText } from "../lib/presentation";
import { useUnhandledRejection } from "./useUnhandledRejection";

afterEach(cleanup);

/** jsdom has no PromiseRejectionEvent, so build the same shape by hand. */
function reject(reason: unknown) {
  const event = new Event("unhandledrejection");
  Object.assign(event, { reason });
  window.dispatchEvent(event);
}

it("reports an unhandled rejection through the error notice", () => {
  const report = vi.fn();
  renderHook(() => useUnhandledRejection(report));
  const failure = new Error("boom");
  reject(failure);
  expect(report).toHaveBeenCalledOnce();
  expect(report).toHaveBeenCalledWith(errorText(failure));
  reject("not an Error");
  expect(report).toHaveBeenLastCalledWith(errorText("not an Error"));
});

it("ignores canceled requests and expired logins", () => {
  const report = vi.fn();
  renderHook(() => useUnhandledRejection(report));
  reject(new DOMException("aborted", "AbortError"));
  reject(new APIError("unauthorized", 401));
  expect(report).not.toHaveBeenCalled();
  // Other API failures are still real problems.
  reject(new APIError("server error", 500));
  expect(report).toHaveBeenCalledOnce();
});

it("always calls the newest handler and stops listening on unmount", () => {
  const first = vi.fn();
  const second = vi.fn();
  const { rerender, unmount } = renderHook(
    ({ report }) => useUnhandledRejection(report),
    { initialProps: { report: first } },
  );
  rerender({ report: second });
  reject(new Error("after rerender"));
  expect(first).not.toHaveBeenCalled();
  expect(second).toHaveBeenCalledOnce();
  unmount();
  reject(new Error("after unmount"));
  expect(second).toHaveBeenCalledOnce();
});
