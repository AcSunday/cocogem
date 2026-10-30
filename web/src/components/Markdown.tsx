import { cx } from "../styles/classes";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";

export function Markdown({ text }: { text: string }) {
  // Regex to find @mentions and wrap them in a span
  const highlightedText = text.replace(
    /@([a-zA-Z0-9_-]+)/g,
    '<span class="mention" title="成员简介">@$1</span>'
  );

  return (
    <div className={cx("markdown")}>
      <ReactMarkdown
        skipHtml={false} // Enable HTML for the mentions
        remarkPlugins={[remarkGfm]}
        components={{
          a: (props) => (
            <a
              href={props.href}
              title={props.title}
              target="_blank"
              rel="noopener noreferrer"
            >
              {props.children}
            </a>
          ),
          pre: (props) => <pre tabIndex={0}>{props.children}</pre>,
        }}
      >
        {highlightedText}
      </ReactMarkdown>
    </div>
  );
}
