import { useId, useState } from "react";
import { api } from "../../api/client";
import type { Agent, System, WorkspaceMember } from "../../api/client";
import { scoped } from "../../styles/classes";
import base from "../../styles/base.module.css";
import controls from "../../styles/controls.module.css";
import layout from "../../styles/layout.module.css";
import conversation from "../../styles/conversation.module.css";
import panels from "../../styles/panels.module.css";
import members from "../../styles/member-editor.module.css";
import settings from "../../styles/settings.module.css";
import { errorText } from "../../lib/presentation";
import { Avatar, memberImage } from "../Avatar";
import { Modal } from "../Modal";
import { Select } from "../Select";

const cx = scoped(
  base,
  controls,
  layout,
  conversation,
  panels,
  members,
  settings,
);

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
const executors = ["claude", "codex", "gemini", "pi"];
/** Empty keeps the CLI's own default; only these explicit levels are forwarded. */
const thinkingLevels: Record<string, string[]> = {
  claude: ["low", "medium", "high", "xhigh", "max"],
  codex: ["low", "medium", "high", "xhigh", "ultra", "max"],
  gemini: ["low", "high"],
  pi: ["low", "medium", "high", "xhigh", "max"],
};
const minContextSize = 1024;
const avatarMaxBytes = 512 * 1024;
const avatarMaxDimension = 2048;
const handlePattern = /^[a-z][a-z0-9-]{1,31}$/;
const names = (value: string) =>
  value
    .split(/[,，]/)
    .map((item) => item.trim())
    .filter(Boolean);

const editable = (agent: Agent): Agent =>
  agent.executor && executors.includes(agent.executor)
    ? agent
    : { ...agent, executor: "", model: "default", thinking_level: "" };

/** Reads PNG or JPEG dimensions from the encoded header; null rejects the file. */
function avatarPixelSize(
  uri: string,
): { width: number; height: number } | null {
  const base64 = uri.replace(/^data:image\/(?:png|jpeg);base64,/i, "");
  let bytes: Uint8Array;
  try {
    bytes = Uint8Array.from(atob(base64), (char) => char.charCodeAt(0));
  } catch {
    return null;
  }
  const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const png = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a];
  if (bytes.length >= 24 && png.every((byte, index) => bytes[index] === byte)) {
    const width = view.getUint32(16);
    const height = view.getUint32(20);
    return width && height ? { width, height } : null;
  }
  if (bytes.length >= 4 && bytes[0] === 0xff && bytes[1] === 0xd8) {
    let at = 2;
    while (at + 9 < bytes.length) {
      if (bytes[at] !== 0xff) {
        at++;
        continue;
      }
      const marker = bytes[at + 1];
      if (marker === 0xff) {
        at++;
        continue;
      }
      if (
        marker === 0xd8 ||
        marker === 0xd9 ||
        marker === 0x01 ||
        (marker >= 0xd0 && marker <= 0xd7)
      ) {
        at += 2;
        continue;
      }
      const length = (bytes[at + 2] << 8) | bytes[at + 3];
      if (length < 2) return null;
      const startOfFrame =
        (marker >= 0xc0 && marker <= 0xc3) ||
        (marker >= 0xc5 && marker <= 0xc7) ||
        (marker >= 0xc9 && marker <= 0xcb) ||
        (marker >= 0xcd && marker <= 0xcf);
      if (startOfFrame) {
        const height = (bytes[at + 5] << 8) | bytes[at + 6];
        const width = (bytes[at + 7] << 8) | bytes[at + 8];
        return width && height ? { width, height } : null;
      }
      at += 2 + length;
    }
  }
  return null;
}

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
  const [editing, setEditing] = useState<{ key: string; agent: Agent } | null>(
    null,
  );
  // Keep saved profiles visible on the list even before a parent refresh lands.
  const [savedProfiles, setSavedProfiles] = useState<Record<string, Agent>>({});
  // A parent refresh at the same or newer revision wins over the local copy.
  const profiles = (catalog ?? agents).map((agent) => {
    const saved = savedProfiles[agent.id];
    return saved && saved.revision > agent.revision ? saved : agent;
  });
  for (const saved of Object.values(savedProfiles))
    if (!profiles.some((agent) => agent.id === saved.id)) profiles.push(saved);
  const body = editing ? (
    <MemberEditor
      key={editing.key}
      agent={editing.agent}
      system={system}
      catalog={catalog}
      global={global}
      membership={
        agents.find((agent) => agent.id === editing.agent.id)?.membership ??
        undefined
      }
      saveMembership={saveMembership}
      unlink={unlink}
      save={save}
      onSaved={(agent) =>
        setSavedProfiles((current) => ({ ...current, [agent.id]: agent }))
      }
      onBack={() => setEditing(null)}
    />
  ) : (
    <div className={cx("member-list")} aria-label="成员列表">
      {profiles.map((agent) => (
        <button
          type="button"
          key={agent.id}
          className={cx("member-card")}
          onClick={() => setEditing({ key: agent.id, agent })}
        >
          <Avatar
            name={agent.display_name}
            avatar={agent.avatar}
          />
          <span className={cx("member-info")}>
            <strong>{agent.display_name || "未命名成员"}</strong>
            <span className={cx("member-handle")}>
              @{agent.handle || "未设置"}
            </span>
            <small>
              {agent.role_description || "未填写职责"}
              {catalog && !global
                ? agents.some((item) => item.id === agent.id)
                  ? " · 已加入"
                  : " · 未加入"
                : ""}
            </small>
          </span>
        </button>
      ))}
      <button
        type="button"
        className={cx("member-card add-member")}
        onClick={() => setEditing({ key: "new", agent: blank() })}
      >
        ＋ 新成员
      </button>
    </div>
  );
  return embedded ? (
    body
  ) : (
    <Modal title="成员与称呼" close={close} wide>
      {body}
    </Modal>
  );
}

function MemberEditor({
  agent: initialAgent,
  system,
  catalog,
  global,
  membership,
  saveMembership,
  unlink,
  save,
  onSaved,
  onBack,
}: {
  agent: Agent;
  system: System;
  catalog?: Agent[];
  global?: boolean;
  membership?: WorkspaceMember;
  saveMembership?: (
    agent: Agent,
    member: WorkspaceMember | undefined,
    value: { enabled: boolean; tools: string[] },
  ) => Promise<void>;
  unlink?: (member: WorkspaceMember) => Promise<void>;
  save: (value: Agent) => Promise<Agent>;
  onSaved: (agent: Agent) => void;
  onBack: () => void;
}) {
  const uid = useId();
  const field = (name: string) => `${uid}-${name}`;
  const [value, setValue] = useState<Agent>(() => editable(initialAgent));
  const [aliases, setAliases] = useState(
    (initialAgent.aliases ?? []).join(","),
  );
  const [capabilities, setCapabilities] = useState(
    (initialAgent.capabilities ?? []).join(","),
  );
  const [contextSize, setContextSize] = useState(() =>
    initialAgent.context_size ? String(initialAgent.context_size) : "",
  );
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const [avatarError, setAvatarError] = useState("");
  const [verifying, setVerifying] = useState(false);
  const [verifiedKey, setVerifiedKey] = useState("");
  const [verifyState, setVerifyState] = useState<{
    key: string;
    error: string;
  } | null>(null);
  const levels = thinkingLevels[value.executor ?? ""] ?? [];
  const model = value.model.trim() || "default";
  const geminiNeedsModel = value.executor === "gemini" && model === "default";
  const executorInfo = system.executors?.find(
    (item) => item.id === value.executor,
  );
  const contextText = contextSize.trim();
  const contextNumber = contextText ? Number(contextText) : 0;
  const contextValid =
    !contextText ||
    (Number.isInteger(contextNumber) && contextNumber >= minContextSize);
  const verifyKey = [
    value.executor,
    model,
    contextText ? String(contextNumber) : "",
    value.thinking_level ?? "",
  ].join("|");
  const verified = verifiedKey === verifyKey;
  const verifyError = verifyState?.key === verifyKey ? verifyState.error : "";
  const update = (next: Partial<Agent>) => {
    setSaved(false);
    setError("");
    setValue((current) => ({ ...current, ...next }));
  };
  const uploadAvatar = (file: File | undefined) => {
    setAvatarError("");
    if (!file) return;
    if (file.type !== "image/png" && file.type !== "image/jpeg") {
      setAvatarError("仅支持 PNG 或 JPEG 图片。");
      return;
    }
    if (file.size > avatarMaxBytes) {
      setAvatarError("头像不能超过 512 KiB。");
      return;
    }
    const reader = new FileReader();
    reader.onerror = () => setAvatarError("读取头像失败，请重试。");
    reader.onload = () => {
      const uri = typeof reader.result === "string" ? reader.result : "";
      const size = memberImage(uri) ? avatarPixelSize(uri) : null;
      if (!size) {
        setAvatarError("头像文件不是有效的 PNG 或 JPEG 图片。");
        return;
      }
      if (size.width > avatarMaxDimension || size.height > avatarMaxDimension) {
        setAvatarError("头像尺寸不能超过 2048×2048 像素。");
        return;
      }
      update({ avatar: uri });
    };
    reader.readAsDataURL(file);
  };
  const verify = () => {
    if (!value.executor || verifying) return;
    if (!contextValid) {
      setVerifyState({
        key: verifyKey,
        error:
          "上下文大小限制必须是不小于 1024 的整数（字节），或留空跟随执行 CLI 默认。",
      });
      return;
    }
    if (geminiNeedsModel && value.thinking_level) {
      setVerifyState({
        key: verifyKey,
        error: "Gemini 选择思考等级前必须填写明确的模型。",
      });
      return;
    }
    setVerifying(true);
    setVerifyState(null);
    void api<{ verified_at: string }>("/api/v1/system/verify", "POST", {
      executor: value.executor,
      model,
      ...(contextText ? { context_size: contextNumber } : {}),
      ...(value.thinking_level ? { thinking_level: value.thinking_level } : {}),
    })
      .then(() => setVerifiedKey(verifyKey))
      .catch((failure) =>
        setVerifyState({ key: verifyKey, error: errorText(failure) }),
      )
      .finally(() => setVerifying(false));
  };
  const submit = () => {
    setError("");
    if (!value.display_name.trim()) {
      setError("请填写显示名。");
      return;
    }
    if (!handlePattern.test(value.handle)) {
      setError(
        "Handle 需以小写字母开头，只能包含小写字母、数字或连字符，长度为 2–32。",
      );
      return;
    }
    if (!value.executor) {
      setError("请选择执行 CLI。");
      return;
    }
    if (!contextValid) {
      setError(
        "上下文大小限制必须是不小于 1024 的整数（字节），或留空跟随执行 CLI 默认。",
      );
      return;
    }
    if (geminiNeedsModel && value.thinking_level) {
      setError("Gemini 选择思考等级前必须填写明确的模型。");
      return;
    }
    setSaving(true);
    void save({
      ...value,
      model,
      aliases: names(aliases),
      ...(catalog ? { capabilities: names(capabilities) } : {}),
      context_size: contextText ? contextNumber : undefined,
      thinking_level: value.thinking_level || undefined,
    })
      .then((savedAgent) => {
        setValue(savedAgent);
        setSaved(true);
        if (savedAgent.id) onSaved(savedAgent);
      })
      .catch((failure) => setError(errorText(failure)))
      .finally(() => setSaving(false));
  };
  const membershipActions =
    !global && catalog && value.id && saveMembership && unlink
      ? { saveMembership, unlink }
      : undefined;
  return (
    <>
      <div className={cx("editor-head")}>
        <button
          type="button"
          className={cx("back-button")}
          onClick={onBack}
          disabled={saving}
        >
          ← 返回列表
        </button>
        <h3>{value.id ? "编辑成员" : "新建成员"}</h3>
      </div>
      <form
        className={cx("member-editor")}
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <section className={cx("member-section")}>
          <h3>{catalog ? "全局成员档案" : "成员档案"}</h3>
          {catalog && (
            <p className={cx("member-note")}>
              档案在所有工作空间共享；本空间权限单独授予，实际权限取两者交集。
            </p>
          )}
          <label htmlFor={field("name")}>显示名</label>
          <input
            id={field("name")}
            required
            value={value.display_name}
            onChange={(event) => update({ display_name: event.target.value })}
          />
          <label htmlFor={field("handle")}>Handle</label>
          <input
            id={field("handle")}
            required
            pattern="[a-z][a-z0-9-]{1,31}"
            title="以小写字母开头，只能包含小写字母、数字或连字符，长度为 2–32。"
            value={value.handle}
            onChange={(event) => update({ handle: event.target.value })}
          />
          <label htmlFor={field("aliases")}>别名（用逗号分隔）</label>
          <input
            id={field("aliases")}
            value={aliases}
            onChange={(event) => {
              setSaved(false);
              setAliases(event.target.value);
            }}
          />
          <label htmlFor={field("role")}>职责</label>
          <textarea
            id={field("role")}
            value={value.role_description}
            onChange={(event) =>
              update({ role_description: event.target.value })
            }
          />
          {catalog && (
            <>
              <label htmlFor={field("capabilities")}>
                能力标签（用逗号分隔）
              </label>
              <input
                id={field("capabilities")}
                value={capabilities}
                onChange={(event) => {
                  setSaved(false);
                  setCapabilities(event.target.value);
                }}
              />
            </>
          )}
          <span className={cx("field-label")}>头像</span>
          <div className={cx("avatar-field")}>
            <span className={cx("avatar-preview")}>
              <Avatar
                name={value.display_name || "新成员"}
                avatar={value.avatar}
              />
            </span>
            <div className={cx("avatar-controls")}>
              <div className={cx("avatar-actions")}>
                <label className={cx("avatar-upload")}>
                  {value.avatar ? "替换头像" : "上传头像"}
                  <input
                    className={cx("sr-only")}
                    type="file"
                    accept="image/png,image/jpeg"
                    aria-label="上传头像"
                    onChange={(event) => {
                      uploadAvatar(event.target.files?.[0]);
                      event.target.value = "";
                    }}
                  />
                </label>
                {value.avatar && (
                  <button
                    type="button"
                    className={cx("avatar-remove")}
                    onClick={() => update({ avatar: "" })}
                  >
                    移除头像
                  </button>
                )}
              </div>
              <small>
                PNG/JPEG，最大 512 KiB、2048×2048 像素；留空显示名称首字。
              </small>
              {avatarError && (
                <p role="alert" className={cx("error")}>
                  {avatarError}
                </p>
              )}
            </div>
          </div>
        </section>
        <section className={cx("member-section")}>
          <h3>执行与模型</h3>
          <label htmlFor={field("executor")}>执行 CLI</label>
          <Select
            id={field("executor")}
            required
            value={value.executor}
            onChange={(selected) =>
              update({
                executor: selected,
                model: "default",
                thinking_level: "",
              })
            }
          >
            <option value="">请选择本机 CLI</option>
            {executors.map((id) => {
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
          {!value.executor && (
            <p role="status" className={cx("member-note")}>
              此成员尚未绑定本机 CLI。请选择执行 CLI；原 API
              模型配置不会自动沿用。
            </p>
          )}
          {executorInfo?.reason && (
            <p role="status" className={cx("member-note")}>
              {executorInfo.supported === false
                ? "此 CLI 当前不可执行："
                : "说明："}
              {executorInfo.reason}
            </p>
          )}
          <label htmlFor={field("model")}>模型（留空使用 CLI 默认）</label>
          <div className={cx("verify-row")}>
            <input
              id={field("model")}
              value={value.model === "default" ? "" : value.model}
              placeholder="CLI 默认模型"
              onChange={(event) =>
                update({
                  model: event.target.value || "default",
                  ...(value.executor === "gemini" &&
                  (!event.target.value.trim() ||
                    event.target.value.trim() === "default")
                    ? { thinking_level: "" }
                    : {}),
                })
              }
            />
            <button
              type="button"
              className={cx("secondary")}
              disabled={!value.executor || verifying}
              onClick={verify}
            >
              {verifying ? "验证中…" : "验证模型连接"}
            </button>
          </div>
          <small>
            验证会复用本机 CLI
            的供应商与登录配置，不需要额外登录或密钥；模型留空时按 CLI
            默认模型验证。
          </small>
          <label htmlFor={field("context")}>上下文大小限制</label>
          <input
            id={field("context")}
            type="number"
            min={minContextSize}
            step={1}
            inputMode="numeric"
            value={contextSize}
            placeholder={
              value.executor
                ? `跟随 ${value.executor} 默认`
                : "跟随执行 CLI 默认"
            }
            aria-describedby={field("context-hint")}
            onChange={(event) => {
              setSaved(false);
              setContextSize(event.target.value);
            }}
          />
          <small id={field("context-hint")}>
            留空时由所选 CLI 按其配置的上下文窗口处理；填写字节数后，Cocogem
            会按该值限制本成员的输入大小。
          </small>
          <label htmlFor={field("thinking")}>
            思考等级（留空保持 CLI 默认）
          </label>
          <Select
            id={field("thinking")}
            value={value.thinking_level ?? ""}
            disabled={!value.executor || !levels.length || geminiNeedsModel}
            onChange={(thinking) => update({ thinking_level: thinking })}
          >
            <option value="">保持 CLI 默认</option>
            {levels.map((level) => (
              <option key={level} value={level}>
                {level}
              </option>
            ))}
          </Select>
          {geminiNeedsModel && (
            <small>
              Gemini 选择思考等级前必须填写明确模型，不能留空使用默认。
            </small>
          )}
          <small>留空时沿用 CLI 已有的思考等级配置。</small>
          {verified && (
            <p role="status" className={cx("verify-ok")}>
              已验证当前执行 CLI、模型、上下文大小限制和思考等级。
            </p>
          )}
          {verifyError && (
            <p role="alert" className={cx("error")}>
              {verifyError}
            </p>
          )}
        </section>
        <section className={cx("member-section")}>
          <fieldset>
            <legend>{catalog ? "全局允许工具" : "允许工具"}</legend>
            {allTools.map((tool) => (
              <label className={cx("check-label")} key={tool}>
                <input
                  type="checkbox"
                  checked={value.tools?.includes(tool) ?? false}
                  onChange={(event) =>
                    update({
                      tools: event.target.checked
                        ? [...(value.tools ?? []), tool]
                        : (value.tools?.filter((item) => item !== tool) ?? []),
                    })
                  }
                />
                {tool}
              </label>
            ))}
          </fieldset>
          <label className={cx("check-label")}>
            <input
              type="checkbox"
              checked={value.disabled}
              onChange={(event) => update({ disabled: event.target.checked })}
            />
            {catalog ? "在所有工作空间停用此成员" : "停用此成员"}
          </label>
          <small>工具仅控制 Cocogem 宿主工具，不授予原生命令或 shell。</small>
        </section>
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
      {membershipActions && (
        <WorkspaceMembershipForm
          key={`${value.id}:${membership?.revision ?? 0}`}
          agent={value}
          membership={membership}
          save={(grant) =>
            membershipActions.saveMembership(value, membership, grant)
          }
          unlink={() =>
            membership
              ? membershipActions.unlink(membership)
              : Promise.resolve()
          }
        />
      )}
    </>
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
  const globalTools = agent.tools ?? [];
  const [enabled, setEnabled] = useState(membership?.enabled ?? true);
  const [tools, setTools] = useState(membership?.tools ?? globalTools);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const effective =
    enabled && !agent.disabled
      ? tools.filter((tool) => globalTools.includes(tool))
      : [];
  const submit = (action: () => Promise<void>) => {
    setBusy(true);
    setError("");
    setSaved(false);
    void action()
      .then(() => setSaved(true))
      .catch((failure) => setError(errorText(failure)))
      .finally(() => setBusy(false));
  };
  return (
    <form
      className={cx("membership-form")}
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
        {allTools.map((tool) => {
          const allowed = globalTools.includes(tool);
          const checked = tools.includes(tool);
          return (
            <label className={cx("check-label")} key={tool}>
              <input
                type="checkbox"
                checked={checked}
                disabled={!allowed && !checked}
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
              {!allowed && "（全局未允许）"}
            </label>
          );
        })}
      </fieldset>
      <p className={cx("member-note")}>
        实际权限取本空间与全局配置的交集；当前生效：
        {effective.length ? effective.join("、") : "无"}
        。移出仅撤销本空间访问，保留全局档案与历史记录。
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
