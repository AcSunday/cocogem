import { useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { createPortal } from "react-dom";
import type { Parent, Root, RootContent, Text } from "mdast";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { Avatar } from "./Avatar";
import { scoped } from "../styles/classes";
import controls from "../styles/controls.module.css";

const cx = scoped(controls);

/** Only the fields a rendered mention needs, so run snapshots and profiles both fit. */
export type MentionMember = {
  id: string;
  handle: string;
  display_name: string;
  aliases?: string[] | null;
  role_description?: string;
  avatar?: string;
};

const MENTION_PREFIX = "#cocogem-mention-";
const CARD_GAP = 6;
const VIEW_MARGIN = 8;
const SKIP_PARENTS = new Set([
  "link",
  "linkReference",
  "inlineCode",
  "code",
  "html",
  "definition",
]);

type Entry = { agent: MentionMember; name: string };
type Piece = { text: string } | { mention: Entry; label: string };

const escapeRegExp = (value: string) =>
  value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/** Longest known spelling wins, so @小明 is not split by a member named 小. */
function entriesFor(members: MentionMember[]) {
  const byName = new Map<string, Entry>();
  for (const agent of members) {
    for (const raw of [
      agent.handle,
      agent.display_name,
      ...(agent.aliases ?? []),
    ]) {
      const name = raw?.trim();
      if (!name) continue;
      const key = name.toLowerCase();
      if (!byName.has(key)) byName.set(key, { agent, name });
    }
  }
  return [...byName.values()].sort((a, b) => b.name.length - a.name.length);
}

function matcherFor(entries: Entry[]) {
  if (!entries.length) return undefined;
  const names = entries.map((entry) => escapeRegExp(entry.name)).join("|");
  return new RegExp(
    `(^|[^\\p{L}\\p{N}_.%+\\-@])@(${names})(?![A-Za-z0-9_\\-]|\\.[A-Za-z])`,
    "giu",
  );
}

/**
 * Splits only text nodes. Code, raw HTML, link destinations and link labels are
 * never visited; an unknown @ stays ordinary text. The walker emits phrasing
 * nodes into the existing parent, so nested bold, lists and tables render once.
 */
function splitText(
  value: string,
  matcher: RegExp,
  byName: Map<string, Entry>,
): Piece[] {
  const pieces: Piece[] = [];
  let last = 0;
  for (const match of value.matchAll(matcher)) {
    const boundary = match[1] ?? "";
    const name = match[2] ?? "";
    const entry = byName.get(name.toLowerCase());
    const at = (match.index ?? 0) + boundary.length;
    if (!entry || at < last) continue;
    if (at > last) pieces.push({ text: value.slice(last, at) });
    pieces.push({ mention: entry, label: `@${name}` });
    last = at + name.length + 1;
  }
  if (last < value.length) pieces.push({ text: value.slice(last) });
  return pieces;
}

function rewrite(
  parent: Parent,
  matcher: RegExp,
  byName: Map<string, Entry>,
) {
  const children: RootContent[] = [];
  for (const child of parent.children as RootContent[]) {
    if (child.type === "text") {
      for (const piece of splitText(child.value, matcher, byName)) {
        if ("text" in piece) {
          children.push({ type: "text", value: piece.text } as Text);
          continue;
        }
        const id = encodeURIComponent(piece.mention.agent.id);
        children.push({
          type: "link",
          url: `${MENTION_PREFIX}${id}`,
          children: [{ type: "text", value: piece.label }],
        } as unknown as RootContent);
      }
      continue;
    }
    if (!SKIP_PARENTS.has(child.type) && "children" in child)
      rewrite(child as Parent, matcher, byName);
    children.push(child);
  }
  parent.children = children as Parent["children"];
}

function Mention({
  agent,
  children,
}: {
  agent: MentionMember;
  children: ReactNode;
}) {
  const tooltip = useId();
  const [open, setOpen] = useState(false);
  const anchor = useRef<HTMLSpanElement>(null);
  const card = useRef<HTMLSpanElement>(null);
  const [position, setPosition] = useState({ left: 0, top: 0 });

  useLayoutEffect(() => {
    if (!open || !anchor.current || !card.current) return;
    const place = () => {
      if (!anchor.current || !card.current) return;
      const trigger = anchor.current.getBoundingClientRect();
      const { width, height } = card.current.getBoundingClientRect();
      // Fixed coordinates: prefer above the mention, flip below when the
      // viewport top is too close, then keep the card fully on screen.
      const below = trigger.bottom + CARD_GAP;
      let top = trigger.top - CARD_GAP - height;
      if (top < VIEW_MARGIN) top = below;
      setPosition({
        left: Math.max(
          VIEW_MARGIN,
          Math.min(trigger.left, window.innerWidth - VIEW_MARGIN - width),
        ),
        top: Math.max(
          VIEW_MARGIN,
          Math.min(top, window.innerHeight - VIEW_MARGIN - height),
        ),
      });
    };
    place();
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => {
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
    };
  }, [open]);

  return (
    <span
      className={cx("mention-wrap")}
      onMouseEnter={() => setOpen(true)}
      onMouseLeave={() => setOpen(false)}
    >
      <span
        ref={anchor}
        className={cx("mention")}
        tabIndex={0}
        aria-describedby={open ? tooltip : undefined}
        onFocus={() => setOpen(true)}
        onBlur={() => setOpen(false)}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault();
            setOpen(false);
          }
        }}
      >
        {children}
      </span>
      {open &&
        createPortal(
          <span
            ref={card}
            className={cx("mention-card")}
            role="tooltip"
            id={tooltip}
            style={position}
          >
            <Avatar
              name={agent.display_name}
              avatar={agent.avatar}
            />
            <span className={cx("mention-detail")}>
              <strong>{agent.display_name}</strong>
              <span>@{agent.handle}</span>
              {agent.role_description && <span>{agent.role_description}</span>}
            </span>
          </span>,
          // Native dialogs paint in the top layer, so the card must stay
          // inside one when the mention is inside a modal.
          anchor.current?.closest("dialog") ?? document.body,
        )}
    </span>
  );
}

export function Markdown({
  text,
  members = [],
}: {
  text: string;
  members?: MentionMember[];
}) {
  const entries = useMemo(() => entriesFor(members), [members]);
  const matcher = useMemo(() => matcherFor(entries), [entries]);
  const byName = useMemo(
    () => new Map(entries.map((entry) => [entry.name.toLowerCase(), entry])),
    [entries],
  );
  const byId = useMemo(
    () => new Map(members.map((member) => [member.id, member])),
    [members],
  );
  const plugin = useMemo(
    () => () => (tree: Root) => {
      if (!matcher) return;
      rewrite(tree, matcher, byName);
    },
    [matcher, byName],
  );
  const components: Components = {
    a: ({ href, title, children }) => {
      // Generated mention links carry our private fragment; ordinary links,
      // including their destinations, are passed through unchanged.
      if (href?.startsWith(MENTION_PREFIX)) {
        let id = "";
        try {
          id = decodeURIComponent(href.slice(MENTION_PREFIX.length));
        } catch {
          // A user-written link that happens to share the prefix stays a link.
        }
        const agent = byId.get(id);
        if (agent) return <Mention agent={agent}>{children}</Mention>;
      }
      return (
        <a
          href={href}
          title={title}
          target="_blank"
          rel="noopener noreferrer"
        >
          {children}
        </a>
      );
    },
    pre: (props) => <pre tabIndex={0}>{props.children}</pre>,
  };
  return (
    <div className={cx("markdown")}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm, plugin]}
        components={components}
      >
        {text}
      </ReactMarkdown>
    </div>
  );
}
