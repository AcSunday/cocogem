import { cx } from "./styles";
import ReactMarkdown from "react-markdown";
export function Markdown({ text }: { text: string }) {
  return (
    <div className={cx("markdown")}>
      <ReactMarkdown
        skipHtml
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
        {text}
      </ReactMarkdown>
    </div>
  );
}
