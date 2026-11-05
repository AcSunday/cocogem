import { Select } from "../Select";
import { useState } from "react";
import type { Thread, Agent, Workspace } from "../../api/client";
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
  workflowChoices,
  workflows,
} from "../../lib/presentation";
import { Modal } from "../Modal";

const cx = scoped(base, controls, conversation, panels, members, settings);

export function ThreadForm({
  current,
  agents,
  workspace,
  defaultCoordinatorID = "",
  close,
  save,
  onModeChange,
}: {
  current?: Thread;
  agents: Agent[];
  workspace?: Workspace;
  defaultCoordinatorID?: string;
  close: () => void;
  save: (value: Thread) => Promise<void>;
  onModeChange?: (mode: string) => Promise<void> | void;
}) {
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
      title: "新会话",
      workflow: "free",
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
  const [mode, setMode] = useState(workspace?.execution_mode || "controlled");
  const [savingMode, setSavingMode] = useState(false);
  const changeMode = async (next: string) => {
    if (next === mode || savingMode) return;
    const previous = mode;
    setMode(next);
    setError("");
    if (!onModeChange) return;
    setSavingMode(true);
    try {
      await onModeChange(next);
    } catch (failure) {
      setMode(previous);
      setError(errorText(failure));
    } finally {
      setSavingMode(false);
    }
  };
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
          <legend>会话成员（1–10 位）</legend>
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
          权限模式
          <Select
            value={mode}
            disabled={savingMode}
            onChange={(selected) => void changeMode(selected)}
          >
            <option value="controlled">受控</option>
            <option value="project_autonomous">项目内自主</option>
            <option value="full_access">完全访问</option>
          </Select>
        </label>
        <small>
          变更会立即更新整个项目工作空间；在途执行按新权限边界停止，历史 Run 保留原授权快照。
        </small>
        <label>
          协调者
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
        <label>
          默认工作方式
          <Select
            value={value.workflow}
            onChange={(selected) => setValue({ ...value, workflow: selected })}
          >
            {workflowChoices.map(([key, name]) => (
              <option key={key} value={key}>
                {name}
              </option>
            ))}
            {value.workflow === "code_analysis" && (
              <option value="code_analysis">{workflows.code_analysis}</option>
            )}
          </Select>
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
