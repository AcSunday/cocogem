import { cx } from "./styles";
import { useEffect, useRef, type ReactNode } from "react";

export function Modal({
  title,
  children,
  close,
}: {
  title: string;
  children: ReactNode;
  close: () => void;
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
      onCancel={(event) => {
        event.preventDefault();
        close();
      }}
      aria-label={title}
    >
      <div className={cx("modal-heading")}>
        <h2>{title}</h2>
        <button type="button" aria-label="关闭窗口" onClick={close}>
          ×
        </button>
      </div>
      {children}
    </dialog>
  );
}
