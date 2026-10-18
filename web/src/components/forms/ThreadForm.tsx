import { useState } from "react";
import type { Thread, Agent } from "../../api/client";
import { cx } from "../../styles/classes";
import { errorText, workflows } from "../../lib/presentation";
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
  const [value, setValue] = useState<Thread>(
    current ?? {
      id: "",
      workspace_id: "",
      title: "新会话",
      agent_ids: agents.slice(0, 4).map((a) => a.id),
      coordinator_id: agents[Math.min(2, agents.length - 1)]?.id ?? "",
      workflow: "free",
      revision: 1,
      updated_at: "",
    },
  );
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
                onChange={(event) =>
                  setValue({
                    ...value,
                    agent_ids: event.target.checked
                      ? [...(value.agent_ids ?? []), agent.id]
                      : (value.agent_ids?.filter((id) => id !== agent.id) ??
                        []),
                  })
                }
              />
              {agent.display_name}
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
                  {agent.display_name}
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
        <button className={cx("primary")} disabled={saving}>
          保存会话
        </button>
      </form>
    </Modal>
  );
}
