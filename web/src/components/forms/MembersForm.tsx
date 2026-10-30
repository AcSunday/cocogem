import { Select } from "../Select";
import { useState } from "react";
import type { Agent, System, WorkspaceMember } from "../../api/client";
import { cx } from "../../styles/classes";
import {
  errorText,
  getMemberColorId,
  memberLabel,
  memberPalette,
  randomMemberColorId,
  setMemberColorId,
} from "../../lib/presentation";
import { Avatar, avatars } from "../Avatar";
import { Modal } from "../Modal";

const readOnlyTools = [
  "list_files",
  "read_file",
  "search_content",
  "web_search",
  "fetch_url",
  "read_source",
  "publish_artifact",
];
const engineeringTools = ["write_file", "patch_file", "run_test"];
export const allTools = [...readOnlyTools, ...engineeringTools];
const names = (value: string) =>
  value
    .split(/[,，]/)
    .map((item) => item.trim())
    .filter(Boolean);

export function MembersForm({
  agents,
  system,
  workspaceID,
  catalog,
  global,
  embedded,
  saveMembership,
  unlink,
  close,
  save,
}: {
  agents: Agent[];
  system: System;
  workspaceID: string;
  catalog?: Agent[];
  global?: boolean;
  embedded?: boolean;
  saveMembership?: (
    agent: Agent,
    member: WorkspaceMember | undefined,
    value: { enabled: boolean; tools: string[] },
  ) => Promise<void>;
  unlink?: (member: WorkspaceMember) => Promise<void>;
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
    executor,
    model: "default",
    tools: readOnlyTools,
    disabled: false,
    revision: 1,
  });
  const initial =
    catalog?.find((agent) => agent.id === agents[0]?.id) ??
    agents[0] ??
    catalog?.[0] ??
    blank();
  const editable = (agent: Agent) =>
    agent.executor && ["claude", "codex", "gemini"].includes(agent.executor)
      ? agent
      : { ...agent, executor: "", model: "default" };
  const [value, setValue] = useState<Agent>(editable(initial));
  const [colorId, setColorId] = useState<string>(() =>
    initial.id ? getMemberColorId(initial.id) : randomMemberColorId(),
  );
  const [aliases, setAliases] = useState((initial.aliases ?? []).join(","));
  const [capabilities, setCapabilities] = useState(
    (initial.capabilities ?? []).join(","),
  );
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const [saving, setSaving] = useState(false);
  const executorInfo = system.executors?.find(
    (item) => item.id === value.executor,
  );
  const membership =
    agents.find((agent) => agent.id === value.id)?.membership ?? undefined;
  const body = (
    <>
      {catalog && (
        <p>
          成员档案在所有工作空间共享。职责、能力、CLI
          与全局权限修改后，各工作空间会使用同一份配置。
          {!global && "本空间权限可在下方单独限制。"}
        </p>
      )}
      <div className={cx("member-picker")}>
        {(catalog ?? agents).map((agent) => (
          <button
            key={agent.id}
            onClick={() => {
              setValue(editable(agent));
              setColorId(getMemberColorId(agent.id));
              setAliases((agent.aliases ?? []).join(","));
              setCapabilities((agent.capabilities ?? []).join(","));
              setSaved(false);
              setError("");
            }}
            disabled={saving}
          >
            <Avatar
              name={agent.display_name}
              id={agent.id}
              avatar={agent.avatar}
              small
            />
            {memberLabel(agent)}
            {catalog &&
              !global &&
              (agents.some((item) => item.id === agent.id)
                ? " · 已加入"
                : " · 可加入")}
          </button>
        ))}
        <button
          onClick={() => {
            setValue(blank());
            setColorId(randomMemberColorId());
            setAliases("");
            setCapabilities("");
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
          void save({
            ...value,
            aliases: names(aliases),
            ...(catalog ? { capabilities: names(capabilities) } : {}),
          })
            .then((agent) => {
              if (agent.id) setMemberColorId(agent.id, colorId);
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
            value={aliases}
            onChange={(event) => {
              setSaved(false);
              setAliases(event.target.value);
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
        {catalog && (
          <label>
            能力标签（用逗号分隔）
            <input
              value={capabilities}
              onChange={(event) => {
                setSaved(false);
                setCapabilities(event.target.value);
              }}
            />
          </label>
        )}
        <label>
          专属颜色
          <div className={cx("color-picker")}>
            {memberPalette.map((item) => (
              <button
                key={item.id}
                type="button"
                className={cx(
                  `color-dot ${colorId === item.id ? "selected" : ""}`,
                )}
                style={{ backgroundColor: item.color }}
                title={item.name}
                aria-label={`选择${item.name}`}
                aria-pressed={colorId === item.id}
                onClick={() => {
                  setSaved(false);
                  setColorId(item.id);
                  if (value.id) setMemberColorId(value.id, item.id);
                }}
              />
            ))}
          </div>
        </label>
        <label>
          头像
          <Select
            value={value.avatar ?? ""}
            onChange={(selected) => {
              setSaved(false);
              setValue({ ...value, avatar: selected });
            }}
          >
            <option value="">使用名称首字</option>
            {avatars.map((item) => (
              <option key={item} value={item}>
                {item}
              </option>
            ))}
          </Select>
        </label>
        <label>
          执行 CLI
          <Select
            required
            value={value.executor}
            onChange={(selected) => {
              setSaved(false);
              setValue({
                ...value,
                executor: selected,
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
          </Select>
        </label>
        <label>
          模型（留空使用 CLI 默认）
          <input
            value={value.model === "default" ? "" : value.model}
            placeholder="CLI 默认模型"
            onChange={(event) => {
              setSaved(false);
              setValue({ ...value, model: event.target.value || "default" });
            }}
          />
        </label>
        <p>
          复用本机 CLI
          的模型、供应商与认证配置。已安装不代表连接或模型可用，请在“设置 →
          服务”验证。 下列权限仅控制 Cocogem 的宿主工具，不授予原生命令或任意
          shell。 write_file、patch_file 与 run_test
          只在工程协作的“开发”动作中按角色生效；复核者不会被授予宿主写工具，
          但项目测试代码本身仍可能写入允许的工程路径，这样的测试会改变工程树，
          不能作为已验证交付。
        </p>
        {!value.executor && (
          <p role="status">
            此成员尚未绑定本机 CLI。请选择执行 CLI；原 API
            模型配置不会自动沿用。
          </p>
        )}
        {executorInfo?.reason && (
          <p role="status">
            {executorInfo.supported === false
              ? "此 CLI 当前不可执行："
              : "说明："}
            {executorInfo.reason}
          </p>
        )}
        <fieldset>
          <legend>{catalog ? "全局允许工具" : "允许工具"}</legend>
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
          {catalog ? "在所有工作空间停用此成员" : "停用此成员"}
        </label>
        {error && (
          <p className={cx("error")} role="alert">
            {error}
          </p>
        )}
        {saved && <p role="status">已保存，可以继续编辑此成员。</p>}
        <div className={cx("form-actions")}>
          <button className={cx("primary")} disabled={saved || saving}>
            {saving ? "保存中…" : "保存成员"}
          </button>
        </div>
      </form>
      {catalog && value.id && saveMembership && unlink && (
        <WorkspaceMembershipForm
          key={`${value.id}:${membership?.revision ?? 0}`}
          agent={value}
          membership={membership}
          save={(grant) => saveMembership(value, membership, grant)}
          unlink={() => (membership ? unlink(membership) : Promise.resolve())}
        />
      )}
    </>
  );
  return embedded ? (
    body
  ) : (
    <Modal title="成员与称呼" close={close}>
      {body}
    </Modal>
  );
}

function WorkspaceMembershipForm({
  agent,
  membership,
  save,
  unlink,
}: {
  agent: Agent;
  membership?: WorkspaceMember;
  save: (value: { enabled: boolean; tools: string[] }) => Promise<void>;
  unlink: () => Promise<void>;
}) {
  const [enabled, setEnabled] = useState(membership?.enabled ?? true);
  const [tools, setTools] = useState(membership?.tools ?? agent.tools ?? []);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const submit = (action: () => Promise<void>) => {
    setBusy(true);
    setError("");
    void action()
      .then(() => setSaved(true))
      .catch((error) => setError(errorText(error)))
      .finally(() => setBusy(false));
  };
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        submit(() => save({ enabled, tools }));
      }}
    >
      <fieldset disabled={busy}>
        <legend>本工作空间权限</legend>
        <label className={cx("check-label")}>
          <input
            type="checkbox"
            checked={enabled}
            onChange={(event) => {
              setEnabled(event.target.checked);
              setSaved(false);
            }}
          />
          在本工作空间启用
        </label>
        {allTools.map((tool) => (
          <label className={cx("check-label")} key={tool}>
            <input
              type="checkbox"
              checked={tools.includes(tool)}
              onChange={(event) => {
                setTools(
                  event.target.checked
                    ? [...tools, tool]
                    : tools.filter((item) => item !== tool),
                );
                setSaved(false);
              }}
            />
            {tool}
          </label>
        ))}
      </fieldset>
      <p>
        实际权限取本空间与全局配置的交集。移出仅撤销本空间访问，保留成员档案和历史记录。
      </p>
      {error && (
        <p role="alert" className={cx("error")}>
          {error}
        </p>
      )}
      {saved && <p role="status">本空间成员配置已保存。</p>}
      <div className={cx("form-actions")}>
        {membership && (
          <button type="button" disabled={busy} onClick={() => submit(unlink)}>
            移出本工作空间
          </button>
        )}
        <button className={cx("primary")} disabled={busy}>
          {busy ? "保存中…" : membership ? "保存本空间权限" : "加入本工作空间"}
        </button>
      </div>
    </form>
  );
}
