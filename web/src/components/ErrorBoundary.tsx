import { Component, type ErrorInfo, type ReactNode } from "react";
import { t, useLanguage } from "../lib/i18n";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import styles from "../styles/error-boundary.module.css";

const cx = scoped(base, styles);

/** The detail shown to the reader is bounded so a huge message cannot flood the page. */
const MAX_DETAIL = 300;

function Fallback({ error, retry }: { error: Error; retry: () => void }) {
  useLanguage();
  const detail = (error.message || String(error)).slice(0, MAX_DETAIL);
  return (
    <div className={cx("crash")} role="alert">
      <h1>{t("界面出现了意外错误", "The interface hit an unexpected error")}</h1>
      <p className={cx("crash-note")}>
        {t(
          "已发送的消息和任务保存在本机服务中，不会因此丢失。可以先尝试重新渲染，仍不行再刷新页面。",
          "Messages and tasks you already sent are stored by the local service and are not lost. Try rendering again first; reload the page if that does not help.",
        )}
      </p>
      <pre className={cx("crash-detail")}>{detail}</pre>
      <div className={cx("crash-actions")}>
        <button type="button" className={cx("primary")} onClick={retry}>
          {t("重新渲染", "Render again")}
        </button>
        <button type="button" onClick={() => window.location.reload()}>
          {t("刷新页面", "Reload page")}
        </button>
      </div>
    </div>
  );
}

type Props = {
  children: ReactNode;
  onError?: (error: Error, info: ErrorInfo) => void;
};

type State = { error: Error | null };

/**
 * Catches a render-time crash anywhere below it so the reader gets a readable,
 * recoverable screen instead of a blank page. It does not catch errors thrown in
 * event handlers or promises; those are surfaced by the app's own error notices.
 */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: unknown): State {
    return { error: error instanceof Error ? error : new Error(String(error)) };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("Unhandled render error", error, info.componentStack);
    this.props.onError?.(error, info);
  }

  private retry = () => this.setState({ error: null });

  render() {
    if (this.state.error)
      return <Fallback error={this.state.error} retry={this.retry} />;
    return this.props.children;
  }
}
