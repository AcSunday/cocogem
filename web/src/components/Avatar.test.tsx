import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { Avatar } from "./Avatar";

afterEach(cleanup);

it("renders a built-in avatar and falls back to the name initial", () => {
  render(
    <>
      <Avatar name="布偶猫" avatar="🦊" />
      <Avatar name="缅因猫" avatar="https://example.test/a.svg" />
    </>,
  );
  expect(screen.getByText("🦊")).toBeInTheDocument();
  expect(screen.getByText("缅")).toBeInTheDocument();
  expect(screen.queryByText("https://example.test/a.svg")).toBeNull();
});
