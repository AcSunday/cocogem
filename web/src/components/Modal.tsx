import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import layout from "../styles/layout.module.css";
import panels from "../styles/panels.module.css";
import { useEffect, useRef, type ReactNode } from "react";
import { Icon } from "./Icon";

const cx = scoped(base, layout, panels);

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
  // A file picker's own cancel/close can surface as a backdrop click or an
  // Escape cancel; neither should dismiss the dialog that opened it.
  const backdropPress = useRef(false);
  useEffect(() => {
    const dialog = ref.current;
    const previous = document.activeElement as HTMLElement | null;
    // A text control loses its caret while the dialog holds focus; restore the
    // exact offset so typing continues where the user left it (a Shift+Enter
    // after closing a dialog must not jump the caret to the start).
    const caret =
      previous instanceof HTMLTextAreaElement ||
      previous instanceof HTMLInputElement
        ? { start: previous.selectionStart, end: previous.selectionEnd }
        : undefined;
    dialog?.showModal();
    return () => {
      dialog?.close();
      previous?.focus();
      if (
        caret &&
        (previous instanceof HTMLTextAreaElement ||
          previous instanceof HTMLInputElement)
      ) {
        // `dialog.close()` restores focus asynchronously in Chromium and resets
        // a text control's selection; reapply the caret on the next frame.
        const target = previous;
        const text = target.value;
        queueMicrotask(() =>
          requestAnimationFrame(() => {
            // The user may have typed or selected in the same frame; only a
            // control that still holds the same text gets the old caret back.
            if (document.activeElement !== target) return;
            if (target.value !== text) return;
            target.setSelectionRange(caret.start, caret.end);
          }),
        );
      }
    };
  }, []);
  return (
    <dialog
      ref={ref}
      className={cx(wide ? "wide" : "")}
      onCancel={(event) => {
        event.preventDefault();
        const active = document.activeElement;
        if (active instanceof HTMLInputElement && active.type === "file")
          return;
        close();
      }}
      onPointerDown={(event) => {
        backdropPress.current = event.target === event.currentTarget;
      }}
      onClick={(event) => {
        const backdrop = event.target === event.currentTarget;
        if (backdrop && backdropPress.current) close();
        backdropPress.current = false;
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
