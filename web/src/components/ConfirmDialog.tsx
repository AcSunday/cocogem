import { useState, type ReactNode } from "react";
import { cx } from "../styles/classes";
import { errorText } from "../lib/presentation";
import { Modal } from "./Modal";

/** Destructive actions always pass through an explicit, labelled confirmation. */
export function ConfirmDialog({
  title,
  children,
  action,
  close,
  confirm,
}: {
  title: string;
  children: ReactNode;
  action: string;
  close: () => void;
  confirm: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return (
    <Modal title={title} close={close}>
      {children}
      {error && (
        <p className={cx("error")} role="alert">
          {error}
        </p>
      )}
      <div className={cx("form-actions")}>
        <button type="button" onClick={close} disabled={busy}>
          取消
        </button>
        <button
          type="button"
          className={cx("danger")}
          disabled={busy}
          onClick={() => {
            setBusy(true);
            setError("");
            void confirm()
              .catch((error) => setError(errorText(error)))
              .finally(() => setBusy(false));
          }}
        >
          {busy ? "正在删除…" : action}
        </button>
      </div>
    </Modal>
  );
}
