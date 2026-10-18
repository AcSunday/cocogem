import { useState } from "react";
import type { Agent, System } from "../../api/client";
import { cx } from "../../styles/classes";
import { errorText } from "../../lib/presentation";
import { Modal } from "../Modal";

const allTools = [
  "list_files",
  "read_file",
  "search_content",
  "web_search",
  "fetch_url",
  "read_source",
  "publish_artifact",
];

export function MembersForm({
  agents,
  system,
  workspaceID,
  close,
  save,
}: {
  agents: Agent[];
  system: System;
  workspaceID: string;
  close: () => void;
  save: (value: Agent) => Promise<void>;
}) {
  const blank = (): Agent => ({
    id: "",
    workspace_id: workspaceID,
    display_name: "",
    handle: "",
    aliases: [],
    role_description: "",
    model: system.models[0],
    tools: allTools,
    disabled: false,
    revision: 1,
  });
  const [value, setValue] = useState<Agent>(agents[0] ?? blank());
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  return (
    <Modal title="成员与称呼" close={close}>
      <div className={cx("member-picker")}>
        {agents.map((agent) => (
          <button
            key={agent.id}
            onClick={() => {
              setValue(agent);
              setSaved(false);
            }}
          >
            {agent.display_name}
          </button>
        ))}
        <button
          onClick={() => {
            setValue(blank());
            setSaved(false);
          }}
        >
          ＋ 新成员
        </button>
      </div>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          setError("");
          void save(value)
            .then(() => setSaved(true))
            .catch((error) => setError(errorText(error)));
        }}
      >
        <label>
          显示名
          <input
            required
            value={value.display_name}
            onChange={(event) => {
              setSaved(false);
              setValue({ ...value, display_name: event.target.value });
            }}
          />
        </label>
        <label>
          Handle
          <input
            required
            pattern="[a-z][a-z0-9-]{1,31}"
            value={value.handle}
            onChange={(event) => {
              setSaved(false);
              setValue({ ...value, handle: event.target.value });
            }}
          />
        </label>
        <label>
          别名（用逗号分隔）
          <input
            value={(value.aliases ?? []).join(",")}
            onChange={(event) => {
              setSaved(false);
              setValue({
                ...value,
                aliases: event.target.value
                  .split(/[,，]/)
                  .map((s) => s.trim())
                  .filter(Boolean),
              });
            }}
          />
        </label>
        <label>
          职责
          <textarea
            value={value.role_description}
            onChange={(event) => {
              setSaved(false);
              setValue({ ...value, role_description: event.target.value });
            }}
          />
        </label>
        <label>
          模型
          <select
            value={value.model}
            onChange={(event) => {
              setSaved(false);
              setValue({ ...value, model: event.target.value });
            }}
          >
            {system.models.map((model) => (
              <option key={model}>{model}</option>
            ))}
          </select>
        </label>
        <fieldset>
          <legend>允许工具</legend>
          {allTools.map((tool) => (
            <label className={cx("check-label")} key={tool}>
              <input
                type="checkbox"
                checked={value.tools?.includes(tool) ?? false}
                onChange={(event) => {
                  setSaved(false);
                  setValue({
                    ...value,
                    tools: event.target.checked
                      ? [...(value.tools ?? []), tool]
                      : (value.tools?.filter((t) => t !== tool) ?? []),
                  });
                }}
              />
              {tool}
            </label>
          ))}
        </fieldset>
        <label className={cx("check-label")}>
          <input
            type="checkbox"
            checked={value.disabled}
            onChange={(event) => {
              setSaved(false);
              setValue({ ...value, disabled: event.target.checked });
            }}
          />
          停用此成员
        </label>
        {error && (
          <p className={cx("error")} role="alert">
            {error}
          </p>
        )}
        {saved && (
          <p role="status">
            已保存。继续编辑前可重新选择该成员以读取最新版本。
          </p>
        )}
        <button className={cx("primary")} disabled={saved}>
          保存成员
        </button>
      </form>
    </Modal>
  );
}
