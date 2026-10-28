import { render, screen } from "@testing-library/react";
import { expect, it } from "vitest";
import { Markdown } from "./Markdown";
it("renders useful Markdown without executing raw HTML or unsafe links", () => {
  const { container } = render(
    <Markdown
      text={
        "# Report\n\n[Source](https://example.com)\n\n<script>alert(1)</script>\n\n[bad](javascript:alert(1))"
      }
    />,
  );
  expect(screen.getByRole("heading", { name: "Report" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Source" })).toHaveAttribute(
    "rel",
    "noopener noreferrer",
  );
  expect(container.querySelector("script")).toBeNull();
  expect(container.querySelector('[href^="javascript:"]')).toBeNull();
});

it("renders GFM tables, task lists and strikethrough from agent replies", () => {
  const { container } = render(
    <Markdown
      text={"| 公司 | 薪资 |\n| --- | --- |\n| DeBank | 30k |\n\n- [x] 已完成\n\n~~旧结论~~"}
    />,
  );
  expect(container.querySelector("table")).not.toBeNull();
  expect(screen.getByRole("cell", { name: "DeBank" })).toBeInTheDocument();
  expect(container.querySelector('input[type="checkbox"]')).not.toBeNull();
  expect(container.querySelector("del")).not.toBeNull();
});
