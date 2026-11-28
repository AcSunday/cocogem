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

it("parses file links, inline code paths, and local markdown images without breaking URLs or times", () => {
  const openedFiles: Array<{ path: string; startLine?: number; endLine?: number }> = [];
  const openedImages: Array<{ src: string; title: string }> = [];
  render(
    <Markdown
      threadID="th-1"
      sources={[
        {
          id: "src-img-1",
          workspace_id: "ws-1",
          thread_id: "th-1",
          run_id: "run-1",
          kind: "image",
          title: "home.png",
          url: "",
          path: "captures/home.png",
          start_line: 0,
          end_line: 0,
          sha256: "",
          content: "",
          truncated: false,
          captured_at: "2026-11-28T00:00:00Z",
          media_type: "image/png",
        },
      ]}
      onOpenFile={(ref) => openedFiles.push(ref)}
      onOpenImage={(img) => openedImages.push(img)}
      text={
        "请看 `internal/httpapi/thread_files.go:10-20` 和 [第5行](file:///repo/README.md#L5) 以及 src/app.tsx:42。\n\n" +
        "时间 10:30 访问 https://example.com/a.ts:12 保持原样。\n\n" +
        "![验收截图](captures/home.png)"
      }
    />,
  );

  fireEvent.click(screen.getByText("internal/httpapi/thread_files.go:10-20"));
  fireEvent.click(screen.getByRole("link", { name: "第5行" }));
  fireEvent.click(screen.getByRole("button", { name: "src/app.tsx:42" }));
  expect(openedFiles).toEqual([
    { path: "internal/httpapi/thread_files.go", startLine: 10, endLine: 20 },
    { path: "/repo/README.md", startLine: 5, label: "第5行" },
    { path: "src/app.tsx", startLine: 42 },
  ]);

  // Web URL stays a normal external link, not intercepted by onOpenFile.
  const webLink = screen.getByRole("link", { name: "https://example.com/a.ts:12" });
  expect(webLink).toHaveAttribute("target", "_blank");
  expect(screen.queryByRole("button", { name: "10:30" })).toBeNull();

  // Markdown image referencing a captured image source uses inline source URL & opens lightbox.
  const img = screen.getByAltText("验收截图");
  expect(img).toHaveAttribute("src", "/api/v1/sources/src-img-1/original?inline=1");
  fireEvent.click(img.closest("button")!);
  expect(openedImages).toHaveLength(1);
  expect(openedImages[0]?.title).toBe("验收截图");
});
