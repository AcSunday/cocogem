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
