import { cx } from "../styles/classes";
import { useEffect, useRef, type ReactNode } from "react";
import { Icon } from "./Icon";

export function Modal({
  title,
  children,
  close,
  wide,
}: {
  title: string;
  children: ReactNode;
  close: () => void;
  wide?: boolean;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current;
    const previous = document.activeElement as HTMLElement | null;
    dialog?.showModal();
    return () => {
      dialog?.close();
      previous?.focus();
    };
  }, []);
  return (
    <dialog
      ref={ref}
      className={cx(wide ? "wide" : "")}
      onCancel={(event) => {
        event.preventDefault();
        close();
      }}
      // A click that lands on the dialog itself is on the backdrop.
      onClick={(event) => {
        if (event.target === event.currentTarget) close();
      }}
      aria-label={title}
    >
      <div className={cx("modal-inner")}>
        <div className={cx("modal-heading")}>
          <h2>{title}</h2>
          <button
            type="button"
            className={cx("icon-button")}
            aria-label="关闭窗口"
            onClick={close}
          >
            <Icon name="close" size={18} />
          </button>
        </div>
        {children}
      </div>
    </dialog>
  );
}
