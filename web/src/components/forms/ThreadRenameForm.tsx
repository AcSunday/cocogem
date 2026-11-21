import { useState } from "react";
import type { Thread } from "../../api/client";
import { t, useLanguage } from "../../lib/i18n";
import { scoped } from "../../styles/classes";
import base from "../../styles/base.module.css";
import controls from "../../styles/controls.module.css";
import conversation from "../../styles/conversation.module.css";
import panels from "../../styles/panels.module.css";
import settings from "../../styles/settings.module.css";
import { errorText } from "../../lib/presentation";
import { Modal } from "../Modal";

const cx = scoped(base, controls, conversation, panels, settings);

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
  useLanguage();
  const [title, setTitle] = useState(thread.title);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  return (
    <Modal title={t("重命名会话", "Rename Conversation")} close={close}>
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
          {t("会话标题", "Conversation title")}
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
            {t("取消", "Cancel")}
          </button>
          <button className={cx("primary")} disabled={saving}>
            {saving
              ? t("正在保存…", "Saving…")
              : t("保存名称", "Save title")}
          </button>
        </div>
      </form>
    </Modal>
  );
}
