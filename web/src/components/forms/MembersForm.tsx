import { useState } from "react";
import type { Agent, System } from "../../api/client";
import { cx } from "../../styles/classes";
import { errorText, memberLabel } from "../../lib/presentation";
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
  save: (value: Agent) => Promise<Agent>;
}) {
  const executor =
    system.executors?.find((item) => item.installed && item.supported !== false)
      ?.id ?? "claude";
  const blank = (): Agent => ({
    id: "",
    workspace_id: workspaceID,
    display_name: "",
    handle: "",
    aliases: [],
    role_description: "",
    executor: system.demo ? undefined : executor,
    model: system.demo ? system.models[0] : "default",
    tools: allTools,
    disabled: false,
    revision: 1,
  });
  const initial = agents[0] ?? blank();
  const editable = (agent: Agent) =>
    system.demo ||
    (agent.executor && ["claude", "codex", "gemini"].includes(agent.executor))
      ? agent
      : { ...agent, executor: "", model: "default" };
  const [value, setValue] = useState<Agent>(editable(initial));
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const [saving, setSaving] = useState(false);
  return (
    <Modal title="成员与称呼" close={close}>
      <div className={cx("member-picker")}>
        {agents.map((agent) => (
          <button
            key={agent.id}
            onClick={() => {
              setValue(editable(agent));
              setSaved(false);
              setError("");
            }}
            disabled={saving}
          >
            {memberLabel(agent)}
          </button>
        ))}
        <button
          onClick={() => {
            setValue(blank());
            setSaved(false);
            setError("");
          }}
          disabled={saving}
        >
          ＋ 新成员
        </button>
      </div>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          setError("");
          setSaving(true);
          void save(value)
            .then((agent) => {
              setValue(agent);
              setSaved(true);
            })
            .catch((error) => setError(errorText(error)))
            .finally(() => setSaving(false));
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
        {!system.demo && (
          <label>
            执行 CLI
            <select
              required
              value={value.executor}
              onChange={(event) => {
                setSaved(false);
                setValue({
                  ...value,
                  executor: event.target.value,
                  model: "default",
                });
              }}
            >
              <option value="">请选择本机 CLI</option>
              {["claude", "codex", "gemini"].map((id) => {
                const info = system.executors?.find((item) => item.id === id);
                return (
                  <option key={id} value={id}>
                    {id} ·{" "}
                    {!info?.installed
                      ? "未检测到"
                      : info.supported === false
                        ? "当前版本不支持"
                        : "已安装"}
                  </option>
                );
              })}
            </select>
          </label>
        )}
        <label>
          {system.demo ? "模型" : "模型（留空使用 CLI 默认）"}
          {system.demo ? (
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
          ) : (
            <input
              value={value.model === "default" ? "" : value.model}
              placeholder="CLI 默认模型"
              onChange={(event) => {
                setSaved(false);
                setValue({ ...value, model: event.target.value || "default" });
              }}
            />
          )}
        </label>
        {!system.demo && (
          <p>
            复用本机 CLI
            的登录。已安装不代表已登录或模型可用，请在“服务与设置”验证。
            下列权限仅控制 Cocogem 的资料工具，不授予原生命令或写文件权限。
          </p>
        )}
        {!system.demo && !value.executor && (
          <p role="status">
            此成员尚未绑定本机 CLI。请选择执行 CLI；原 API
            模型配置不会自动沿用。
          </p>
        )}
        {!system.demo &&
          system.executors?.find((item) => item.id === value.executor)
            ?.supported === false && (
            <p role="status">
              此 CLI 当前不可执行：
              {
                system.executors.find((item) => item.id === value.executor)
                  ?.reason
              }
            </p>
          )}
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
        {saved && <p role="status">已保存，可以继续编辑此成员。</p>}
        <button className={cx("primary")} disabled={saved || saving}>
          {saving ? "保存中…" : "保存成员"}
        </button>
      </form>
    </Modal>
  );
}
