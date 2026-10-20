import { useState } from "react";
import type { Thread, Agent } from "../../api/client";
import { cx } from "../../styles/classes";
import { errorText, memberLabel, workflows } from "../../lib/presentation";
import { Modal } from "../Modal";

export function ThreadForm({
  current,
  agents,
  close,
  save,
}: {
  current?: Thread;
  agents: Agent[];
  close: () => void;
  save: (value: Thread) => Promise<void>;
}) {
  const [value, setValue] = useState<Thread>(() => {
    const ids = current
      ? (current.agent_ids?.filter((id) =>
          agents.some((agent) => agent.id === id && !agent.disabled),
        ) ?? [])
      : agents
          .filter((agent) => !agent.disabled)
          .slice(0, 4)
          .map((agent) => agent.id);
    return {
      id: "",
      workspace_id: "",
      title: "新会话",
      workflow: "free",
      revision: 1,
      updated_at: "",
      ...current,
      agent_ids: ids,
      coordinator_id:
        current && ids.includes(current.coordinator_id)
          ? current.coordinator_id
          : (ids[Math.min(2, ids.length - 1)] ?? ""),
    };
  });
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  return (
    <Modal title={current ? "会话设置" : "新建会话"} close={close}>
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
          会话标题
          <input
            required
            value={value.title}
            onChange={(event) =>
              setValue({ ...value, title: event.target.value })
            }
          />
        </label>
        <fieldset>
          <legend>会话成员（1–4 位）</legend>
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
          协调者
          <select
            value={value.coordinator_id}
            onChange={(event) =>
              setValue({ ...value, coordinator_id: event.target.value })
            }
          >
            {agents
              .filter((agent) => value.agent_ids?.includes(agent.id))
              .map((agent) => (
                <option key={agent.id} value={agent.id}>
                  {memberLabel(agent)}
                </option>
              ))}
          </select>
        </label>
        <label>
          默认工作方式
          <select
            value={value.workflow}
            onChange={(event) =>
              setValue({ ...value, workflow: event.target.value })
            }
          >
            {Object.entries(workflows).map(([key, name]) => (
              <option key={key} value={key}>
                {name}
              </option>
            ))}
          </select>
        </label>
        {error && (
          <p role="alert" className={cx("error")}>
            {error}
          </p>
        )}
        <div className={cx("form-actions")}>
          <button className={cx("primary")} disabled={saving}>
            保存会话
          </button>
        </div>
      </form>
    </Modal>
  );
}
