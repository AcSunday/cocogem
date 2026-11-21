import { Select } from "../Select";
import { useState } from "react";
import type { Thread, Agent } from "../../api/client";
import { t, useLanguage } from "../../lib/i18n";
import { scoped } from "../../styles/classes";
import base from "../../styles/base.module.css";
import controls from "../../styles/controls.module.css";
import conversation from "../../styles/conversation.module.css";
import panels from "../../styles/panels.module.css";
import members from "../../styles/member-editor.module.css";
import settings from "../../styles/settings.module.css";
import {
  errorText,
  memberLabel,
} from "../../lib/presentation";
import { Modal } from "../Modal";

const cx = scoped(base, controls, conversation, panels, members, settings);

export function ThreadForm({
  current,
  agents,
  defaultCoordinatorID = "",
  close,
  save,
}: {
  current?: Thread;
  agents: Agent[];
  defaultCoordinatorID?: string;
  close: () => void;
  save: (value: Thread) => Promise<void>;
}) {
  useLanguage();
  const [value, setValue] = useState<Thread>(() => {
    const ids = current
      ? (current.agent_ids?.filter((id) =>
          agents.some((agent) => agent.id === id && !agent.disabled),
        ) ?? [])
      : agents
          .filter((agent) => !agent.disabled)
          .sort(
            (a, b) =>
              Number(b.id === defaultCoordinatorID) -
              Number(a.id === defaultCoordinatorID),
          )
          .slice(0, 10)
          .map((agent) => agent.id);
    return {
      id: "",
      workspace_id: "",
      title: t("新会话", "New conversation"),
      revision: 1,
      updated_at: "",
      ...current,
      agent_ids: ids,
      coordinator_id:
        current && ids.includes(current.coordinator_id)
          ? current.coordinator_id
          : ids.includes(defaultCoordinatorID)
            ? defaultCoordinatorID
            : (ids[Math.min(2, ids.length - 1)] ?? ""),
    };
  });
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  return (
    <Modal
      title={
        current
          ? t("会话设置", "Conversation Settings")
          : t("新建会话", "New Conversation")
      }
      close={close}
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          setSaving(true);
          void save(value)
            .catch((error) => setError(errorText(error)))
            .finally(() => setSaving(false));
        }}
      >
        <label>
          {t("会话标题", "Conversation title")}
          <input
            required
            value={value.title}
            onChange={(event) =>
              setValue({ ...value, title: event.target.value })
            }
          />
        </label>
        <fieldset>
          <legend>
            {t("会话成员（1–10 位）", "Conversation members (1–10)")}
          </legend>
          {agents.map((agent) => (
            <label className={cx("check-label")} key={agent.id}>
              <input
                type="checkbox"
                checked={value.agent_ids?.includes(agent.id) ?? false}
                onChange={(event) => {
                  const ids = event.target.checked
                    ? [...(value.agent_ids ?? []), agent.id]
                    : (value.agent_ids?.filter((id) => id !== agent.id) ?? []);
                  setValue({
                    ...value,
                    agent_ids: ids,
                    coordinator_id: ids.includes(value.coordinator_id)
                      ? value.coordinator_id
                      : (ids[0] ?? ""),
                  });
                }}
              />
              {memberLabel(agent)}
            </label>
          ))}
        </fieldset>
        <label>
          {t("协调者", "Coordinator")}
          <Select
            value={value.coordinator_id}
            onChange={(selected) =>
              setValue({ ...value, coordinator_id: selected })
            }
          >
            {agents
              .filter((agent) => value.agent_ids?.includes(agent.id))
              .map((agent) => (
                <option key={agent.id} value={agent.id}>
                  {memberLabel(agent)}
                </option>
              ))}
          </Select>
        </label>
        {error && (
          <p role="alert" className={cx("error")}>
            {error}
          </p>
        )}
        <div className={cx("form-actions")}>
          <button className={cx("primary")} disabled={saving}>
            {t("保存会话", "Save conversation")}
          </button>
        </div>
      </form>
    </Modal>
  );
}
