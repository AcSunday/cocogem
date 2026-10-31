import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { Markdown, type MentionMember } from "./Markdown";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});
const rect = (top: number, left: number, width: number, height: number) =>
  ({
    top,
    left,
    width,
    height,
    bottom: top + height,
    right: left + width,
  }) as unknown as DOMRect;
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

const avatar = "data:image/png;base64,iVBORw0KGgo=";

const members: MentionMember[] = [
  {
    id: "ragdoll",
    handle: "ragdoll",
    display_name: "布偶猫",
    aliases: ["布布"],
    role_description: "研究资料并核对证据。",
    avatar,
  },
];

it("highlights only known member mentions and keeps code, links and emails intact", () => {
  const { container } = render(
    <Markdown
      members={members}
      text={
        "请 @ragdoll 和 @布布 复核，@unknown 保持普通。\n\n`@ragdoll`\n\n[资料 @ragdoll](https://example.com/@ragdoll)\n\nragdoll@example.com"
      }
    />,
  );
  const mentions = [...container.querySelectorAll('span[tabindex="0"]')];
  expect(mentions.map((node) => node.textContent)).toEqual([
    "@ragdoll",
    "@布布",
  ]);
  expect(screen.getByText(/@unknown 保持普通/)).toBeVisible();
  expect(container.querySelector("code")).toHaveTextContent("@ragdoll");
  expect(screen.queryByText("资料 @ragdoll")).not.toBeNull();
  expect(container.querySelectorAll('span[tabindex="0"]')).toHaveLength(2);
  const link = screen.getByRole("link", { name: "资料 @ragdoll" });
  expect(link).toHaveAttribute("href", "https://example.com/@ragdoll");
  expect(screen.getByText("ragdoll@example.com")).toBeVisible();
});

it("reveals the real member avatar, name, handle and role on hover or focus", () => {
  render(<Markdown members={members} text="请 @ragdoll 复核" />);
  const mention = screen.getByText("@ragdoll");
  expect(screen.queryByRole("tooltip")).toBeNull();
  fireEvent.focus(mention);
  const tooltip = screen.getByRole("tooltip");
  expect(tooltip).toHaveTextContent("布偶猫");
  expect(tooltip).toHaveTextContent("@ragdoll");
  expect(tooltip).toHaveTextContent("研究资料并核对证据。");
  expect(tooltip.querySelector("img")).toHaveAttribute("src", avatar);
  fireEvent.keyDown(mention, { key: "Escape" });
  expect(screen.queryByRole("tooltip")).toBeNull();
  fireEvent.mouseEnter(mention.parentElement as HTMLElement);
  expect(screen.getByRole("tooltip")).toBeVisible();
  fireEvent.mouseLeave(mention.parentElement as HTMLElement);
  expect(screen.queryByRole("tooltip")).toBeNull();
});

it("keeps the mention card in the viewport, flipping below and clamping at the edge", () => {
  const width = vi
    .spyOn(window, "innerWidth", "get")
    .mockReturnValue(1024);
  vi.spyOn(window, "innerHeight", "get").mockReturnValue(768);
  const anchor = { top: 300, left: 100 };
  vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(
    function (this: Element) {
      if (this.getAttribute("role") === "tooltip") return rect(0, 0, 200, 80);
      if (this.getAttribute("tabindex") === "0")
        return rect(anchor.top, anchor.left, 60, 20);
      return rect(0, 0, 0, 0);
    },
  );

  render(<Markdown members={members} text="请 @ragdoll 复核" />);
  const mention = screen.getByText("@ragdoll");
  fireEvent.focus(mention);
  const card = screen.getByRole("tooltip");
  expect(card.parentElement).toBe(document.body);
  expect(card.style.top).toBe("214px");
  expect(card.style.left).toBe("100px");

  // No room above the mention: the card flips below it.
  fireEvent.blur(mention);
  anchor.top = 20;
  fireEvent.focus(mention);
  expect(screen.getByRole("tooltip").style.top).toBe("46px");

  // A mention near the right edge pulls the card back inside the viewport.
  fireEvent.blur(mention);
  width.mockReturnValue(400);
  anchor.top = 300;
  anchor.left = 380;
  fireEvent.focus(mention);
  expect(screen.getByRole("tooltip").style.left).toBe("192px");
});

it("mentions render once inside nested bold and list items", () => {
  const { container } = render(
    <Markdown members={members} text={"- **@布偶猫** 复核\n- @ragdoll 研究"} />,
  );
  expect(container.querySelectorAll('span[tabindex="0"]')).toHaveLength(2);
  expect(container.querySelector('strong span[tabindex="0"]')).toHaveTextContent(
    "@布偶猫",
  );
});
