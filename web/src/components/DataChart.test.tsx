import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it } from "vitest";
import { DataChart } from "./DataChart";

afterEach(cleanup);

it("rebuilds supported chart elements without injecting arbitrary SVG", () => {
  const { container } = render(
    <DataChart source={`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 800 320" onload="alert(1)" aria-label="CSV chart"><title>Amount</title><rect width="800" height="320" fill="white"/><circle cx="36" cy="284" r="3" fill="#2563eb" onclick="alert(1)"><title>row 2: 2</title><script>alert(1)</script></circle><foreignObject><div xmlns="http://www.w3.org/1999/xhtml">bad</div></foreignObject><text x="36" y="24">n=1</text></svg>`} />,
  );
  expect(screen.getByTestId("data-chart")).toBeInTheDocument();
  expect(container.querySelector("circle")).toHaveAttribute("cx", "36");
  expect(container.querySelector("circle")).not.toHaveAttribute("onclick");
  expect(container.querySelector("svg")).not.toHaveAttribute("onload");
  expect(container.querySelector("script")).toBeNull();
  expect(container.querySelector("foreignObject")).toBeNull();
  expect(screen.queryByText("bad")).not.toBeInTheDocument();
});

it("shows malformed chart input as inert text", () => {
  render(<DataChart source="not an SVG" />);
  expect(screen.queryByTestId("data-chart")).not.toBeInTheDocument();
  expect(screen.getByText("not an SVG")).toBeInTheDocument();
});
