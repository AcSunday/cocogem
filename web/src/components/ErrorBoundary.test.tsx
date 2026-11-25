import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ErrorBoundary } from "./ErrorBoundary";

let shouldThrow = true;

function Bomb() {
  if (shouldThrow) throw new Error("render exploded");
  return <p>healthy subtree</p>;
}

beforeEach(() => {
  shouldThrow = true;
  // React logs every caught render error; keep the test output readable.
  vi.spyOn(console, "error").mockImplementation(() => {});
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

it("renders children untouched when nothing throws", () => {
  shouldThrow = false;
  render(
    <ErrorBoundary>
      <Bomb />
    </ErrorBoundary>,
  );
  expect(screen.getByText("healthy subtree")).toBeInTheDocument();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});

it("replaces a crashed subtree with a fallback and reports the error", () => {
  const onError = vi.fn();
  render(
    <ErrorBoundary onError={onError}>
      <Bomb />
    </ErrorBoundary>,
  );
  expect(screen.getByRole("alert")).toHaveTextContent("render exploded");
  expect(screen.getByRole("button", { name: "刷新页面" })).toBeInTheDocument();
  expect(onError).toHaveBeenCalledOnce();
  expect(onError.mock.calls[0][0]).toBeInstanceOf(Error);
});

it("renders the subtree again once the cause is gone", () => {
  render(
    <ErrorBoundary>
      <Bomb />
    </ErrorBoundary>,
  );
  expect(screen.getByRole("alert")).toBeInTheDocument();
  shouldThrow = false;
  fireEvent.click(screen.getByRole("button", { name: "重新渲染" }));
  expect(screen.getByText("healthy subtree")).toBeInTheDocument();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});
