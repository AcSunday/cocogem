import { useState } from "react";
import type { Thread } from "../../api/client";
import { cx } from "../../styles/classes";
import { errorText } from "../../lib/presentation";
import { Modal } from "../Modal";

/** Renaming stays a title-only edit; members and workflow keep their settings. */
export function ThreadRenameForm({
  thread,
  close,
  save,
}: {
  thread: Thread;
  close: () => void;
  save: (title: string) => Promise<void>;
}) {
  const [title, setTitle] = useState(thread.title);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  return (
    <Modal title="重命名会话" close={close}>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          setSaving(true);
          setError("");
          // A failed save keeps this draft and its error in the open dialog.
          void save(title.trim())
            .catch((error) => setError(errorText(error)))
            .finally(() => setSaving(false));
        }}
      >
        <label>
          会话标题
          <input
            required
            value={title}
            onChange={(event) => setTitle(event.target.value)}
          />
        </label>
        {error && (
          <p role="alert" className={cx("error")}>
            {error}
          </p>
        )}
        <div className={cx("form-actions")}>
          <button type="button" disabled={saving} onClick={close}>
            取消
          </button>
          <button className={cx("primary")} disabled={saving}>
            {saving ? "正在保存…" : "保存名称"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
