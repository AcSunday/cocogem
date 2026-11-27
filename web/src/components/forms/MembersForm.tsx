import { useEffect, useId, useState } from "react";
import { api } from "../../api/client";
import type {
  Agent,
  System,
  WorkspaceMember,
  ModelOptions,
} from "../../api/client";
import { scoped } from "../../styles/classes";
import base from "../../styles/base.module.css";
import controls from "../../styles/controls.module.css";
import layout from "../../styles/layout.module.css";
import conversation from "../../styles/conversation.module.css";
import panels from "../../styles/panels.module.css";
import members from "../../styles/member-editor.module.css";
import settings from "../../styles/settings.module.css";
import { errorText } from "../../lib/presentation";
import { t, useLanguage } from "../../lib/i18n";
import {
  CAT_PRESETS,
  firstUnusedCatAvatar,
  usedCatAvatarOwners,
} from "../../lib/cats";
import { Avatar, memberImage } from "../Avatar";
import { CatPortrait } from "../CatSprite";
import { ConfirmDialog } from "../ConfirmDialog";
import { Icon } from "../Icon";
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

const executors = ["claude", "codex", "agy", "gemini", "pi", "opencode"];
/** Empty keeps the CLI's own default; only these explicit levels are forwarded. */
const thinkingLevels: Record<string, string[]> = {
  claude: ["low", "medium", "high", "xhigh", "max"],
  codex: ["low", "medium", "high", "xhigh", "ultra", "max"],
  agy: ["low", "medium", "high", "xhigh", "max"],
  gemini: ["low", "high"],
  pi: ["low", "medium", "high", "xhigh", "max"],
};
const minContextTokens = 1024;
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
  remove,
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
    value: { enabled: boolean },
  ) => Promise<void>;
  unlink?: (member: WorkspaceMember) => Promise<void>;
  close: () => void;
  save: (value: Agent) => Promise<Agent>;
  remove?: (agent: Agent) => Promise<void>;
}) {
  useLanguage();
  const executor =
    system.executors?.find((item) => item.installed && item.supported !== false)
      ?.id ?? "claude";
  const blank = (existing: Agent[]): Agent => ({
    id: "",
    workspace_id: workspaceID,
    display_name: "",
    handle: "",
    avatar: firstUnusedCatAvatar(existing),
    aliases: [],
    role_description: "",
    executor,
    model: "default",
    // Tool grants are no longer edited here. New profiles keep the server's
    // legacy default catalog as audit data; the executed boundary comes from
    // the global permission mode.
    tools: null,
    disabled: false,
    revision: 1,
  });
  const [editing, setEditing] = useState<{ key: string; agent: Agent } | null>(
    null,
  );
  // Keep saved profiles visible on the list even before a parent refresh lands.
  const [savedProfiles, setSavedProfiles] = useState<Record<string, Agent>>({});
  const [deleted, setDeleted] = useState<Set<string>>(() => new Set());
  const [deleting, setDeleting] = useState<Agent>();
  // A parent refresh at the same or newer revision wins over the local copy.
  const profiles = (catalog ?? agents)
    .filter((agent) => !deleted.has(agent.id))
    .map((agent) => {
      const saved = savedProfiles[agent.id];
      return saved && saved.revision > agent.revision ? saved : agent;
    });
  for (const saved of Object.values(savedProfiles))
    if (!deleted.has(saved.id) && !profiles.some((agent) => agent.id === saved.id))
      profiles.push(saved);
  const body = editing ? (
    <MemberEditor
      key={editing.key}
      agent={editing.agent}
      existingProfiles={profiles}
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
    <div
      className={cx("member-list")}
      aria-label={t("成员列表", "Member list")}
    >
      {profiles.map((agent) => (
        <div className={cx("member-row")} key={agent.id}>
          <button
            type="button"
            className={cx("member-card")}
            onClick={() => setEditing({ key: agent.id, agent })}
          >
            <Avatar
              name={agent.display_name}
              avatar={agent.avatar}
              executor={agent.executor}
            />
            <span className={cx("member-info")}>
              <strong>
                {agent.display_name || t("未命名成员", "Unnamed member")}
              </strong>
              <span className={cx("member-handle")}>
                @{agent.handle || t("未设置", "unset")}
              </span>
              <small>
                {agent.role_description || t("未填写职责", "No role description")}
                {catalog && !global
                  ? agents.some((item) => item.id === agent.id)
                    ? t(" · 已加入", " · Joined")
                    : t(" · 未加入", " · Not joined")
                  : ""}
              </small>
            </span>
          </button>
          {remove && (
            <button
              type="button"
              className={cx("member-delete")}
              aria-label={t(
                `删除成员 ${agent.display_name || "未命名成员"}`,
                `Delete member ${agent.display_name || "Unnamed member"}`,
              )}
              title={t("删除成员", "Delete member")}
              onClick={() => setDeleting(agent)}
            >
              <Icon name="trash" size={15} />
            </button>
          )}
        </div>
      ))}
      <button
        type="button"
        className={cx("member-card add-member")}
        onClick={() => setEditing({ key: "new", agent: blank(profiles) })}
      >
        {t("＋ 新成员", "＋ New member")}
      </button>
    </div>
  );
  return (
    <>
      {embedded ? (
        body
      ) : (
        <Modal
          title={t("成员与称呼", "Members & Handles")}
          close={close}
          wide
        >
          {body}
        </Modal>
      )}
      {deleting && remove && (
        <ConfirmDialog
          title={t("删除成员", "Delete Member")}
          action={t("确认删除", "Confirm delete")}
          close={() => setDeleting(undefined)}
          confirm={async () => {
            await remove(deleting);
            setDeleted((current) => new Set(current).add(deleting.id));
            setSavedProfiles((current) => {
              const next = { ...current };
              delete next[deleting.id];
              return next;
            });
            if (editing?.agent.id === deleting.id) setEditing(null);
            setDeleting(undefined);
          }}
        >
          <p>
            {t(
              `删除成员「${deleting.display_name || "未命名成员"}」？`,
              `Delete member “${deleting.display_name || "Unnamed member"}”?`,
            )}
          </p>
          <p className={cx("muted")}>
            {t(
              "该成员的全局档案和所有工作空间关系会被移除；历史消息、Run 快照和已完成任务记录保留不变。此操作无法撤销。",
              "This removes the member's global profile and project links; historical messages, run snapshots, and completed task records are preserved. This action cannot be undone.",
            )}
          </p>
        </ConfirmDialog>
      )}
    </>
  );
}

function MemberEditor({
  agent: initialAgent,
  existingProfiles,
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
  existingProfiles: Agent[];
  system: System;
  catalog?: Agent[];
  global?: boolean;
  membership?: WorkspaceMember;
  saveMembership?: (
    agent: Agent,
    member: WorkspaceMember | undefined,
    value: { enabled: boolean },
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
  const [variantState, setVariantState] = useState<{
    key: string;
    levels: string[];
    error: string;
    loading: boolean;
  }>({ key: "", levels: [], error: "", loading: false });
  const modelKey = value.model.trim() || "default";
  useEffect(() => {
    if (value.executor !== "opencode") return;
    let active = true;
    const timer = setTimeout(() => {
      setVariantState({ key: modelKey, levels: [], error: "", loading: true });
      void api<ModelOptions>(
        `/api/v1/system/model-options?executor=opencode&model=${encodeURIComponent(modelKey)}`,
      )
        .then((options) => {
          if (active)
            setVariantState({
              key: modelKey,
              levels: options.thinking_levels,
              error: "",
              loading: false,
            });
        })
        .catch((failure) => {
          if (active)
            setVariantState({
              key: modelKey,
              levels: [],
              error: errorText(failure),
              loading: false,
            });
        });
    }, 250);
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [value.executor, modelKey]);
  const openCode = value.executor === "opencode";
  const variantCurrent = variantState.key === modelKey;
  const levels = openCode
    ? variantCurrent
      ? variantState.levels
      : []
    : (thinkingLevels[value.executor ?? ""] ?? []);
  const variantLoading = openCode && (!variantCurrent || variantState.loading);
  const variantError = openCode && variantCurrent ? variantState.error : "";
  const levelValid =
    !openCode ||
    !value.thinking_level ||
    (!variantLoading && !variantError && levels.includes(value.thinking_level));
  const model = value.model.trim() || "default";
  const geminiNeedsModel = value.executor === "gemini" && model === "default";
  const executorInfo = system.executors?.find(
    (item) => item.id === value.executor,
  );
  const contextText = contextSize.trim();
  const contextNumber = contextText ? Number(contextText) : 0;
  const contextValid =
    !contextText ||
    (Number.isInteger(contextNumber) && contextNumber >= minContextTokens);
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
      setAvatarError(
        t("仅支持 PNG 或 JPEG 图片。", "Only PNG or JPEG images are supported."),
      );
      return;
    }
    if (file.size > avatarMaxBytes) {
      setAvatarError(
        t("头像不能超过 512 KiB。", "Avatar cannot exceed 512 KiB."),
      );
      return;
    }
    const reader = new FileReader();
    reader.onerror = () =>
      setAvatarError(
        t("读取头像失败，请重试。", "Failed to read avatar image; please try again."),
      );
    reader.onload = () => {
      const uri = typeof reader.result === "string" ? reader.result : "";
      const size = memberImage(uri) ? avatarPixelSize(uri) : null;
      if (!size) {
        setAvatarError(
          t(
            "头像文件不是有效的 PNG 或 JPEG 图片。",
            "The selected file is not a valid PNG or JPEG image.",
          ),
        );
        return;
      }
      if (size.width > avatarMaxDimension || size.height > avatarMaxDimension) {
        setAvatarError(
          t(
            "头像尺寸不能超过 2048×2048 像素。",
            "Avatar dimensions cannot exceed 2048×2048 pixels.",
          ),
        );
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
        error: t(
          "上下文预算必须是不小于 1024 的整数（tokens），或留空跟随执行 CLI 默认。",
          "Context budget must be an integer >= 1024 tokens, or left blank to use the CLI default.",
        ),
      });
      return;
    }
    if (!levelValid) {
      setVerifyState({
        key: verifyKey,
        error: t(
          "当前模型不支持此思考级别，请重新选择。",
          "The current model does not support this thinking level; please choose another.",
        ),
      });
      return;
    }
    if (geminiNeedsModel && value.thinking_level) {
      setVerifyState({
        key: verifyKey,
        error: t(
          "Gemini 选择思考等级前必须填写明确的模型。",
          "Specify an explicit model before selecting a thinking level for Gemini.",
        ),
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
      setError(t("请填写显示名。", "Please enter a display name."));
      return;
    }
    if (!handlePattern.test(value.handle)) {
      setError(
        t(
          "Handle 需以小写字母开头，只能包含小写字母、数字或连字符，长度为 2–32。",
          "Handle must start with a lowercase letter, contain only lowercase letters, numbers, or hyphens, and be 2–32 characters long.",
        ),
      );
      return;
    }
    if (!value.executor) {
      setError(t("请选择执行 CLI。", "Please select a local CLI."));
      return;
    }
    if (!contextValid) {
      setError(
        t(
          "上下文预算必须是不小于 1024 的整数（tokens），或留空跟随执行 CLI 默认。",
          "Context budget must be an integer >= 1024 tokens, or left blank to use the CLI default.",
        ),
      );
      return;
    }
    if (!levelValid) {
      setError(
        t(
          "当前模型不支持此思考级别，请重新选择。",
          "The current model does not support this thinking level; please choose another.",
        ),
      );
      return;
    }
    if (geminiNeedsModel && value.thinking_level) {
      setError(
        t(
          "Gemini 选择思考等级前必须填写明确的模型。",
          "Specify an explicit model before selecting a thinking level for Gemini.",
        ),
      );
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
          {t("← 返回列表", "← Back to list")}
        </button>
        <h3>
          {value.id
            ? t("编辑成员", "Edit member")
            : t("新建成员", "New member")}
        </h3>
      </div>
      <form
        className={cx("member-editor")}
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <section className={cx("member-section")}>
          <h3>
            {catalog
              ? t("全局成员档案", "Global member profile")
              : t("成员档案", "Member profile")}
          </h3>
          {catalog && (
            <p className={cx("member-note")}>
              {t(
                "档案在所有工作空间共享；是否加入、是否启用按工作空间管理，执行边界由全局权限设置决定。",
                "Profiles are shared across all projects; joining and enabling are managed per project, while tool boundaries follow global permission settings.",
              )}
            </p>
          )}
          <label htmlFor={field("name")}>{t("显示名", "Display name")}</label>
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
            title={t(
              "以小写字母开头，只能包含小写字母、数字或连字符，长度为 2–32。",
              "Start with a lowercase letter; use only lowercase letters, numbers, or hyphens (2–32 characters).",
            )}
            value={value.handle}
            onChange={(event) => update({ handle: event.target.value })}
          />
          <label htmlFor={field("aliases")}>
            {t("别名（用逗号分隔）", "Aliases (comma-separated)")}
          </label>
          <input
            id={field("aliases")}
            value={aliases}
            onChange={(event) => {
              setSaved(false);
              setAliases(event.target.value);
            }}
          />
          <label htmlFor={field("role")}>{t("职责", "Role description")}</label>
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
                {t("能力标签（用逗号分隔）", "Capability tags (comma-separated)")}
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
          <span className={cx("field-label")}>{t("头像与猫猫形象", "Avatar & Cat Persona")}</span>
          <div className={cx("cat-picker-wrap")}>
            <div className={cx("cat-picker-head")}>
              <span>
                {t(
                  "内置 10 只猫猫形象（头像与猫猫研发室联动，每只猫猫仅可由一位成员使用）",
                  "10 built-in pixel cats (linked to Cat Studio; each cat can only be held by one member)",
                )}
              </span>
            </div>
            <div
              className={cx("cat-picker")}
              role="group"
              aria-label={t("选择猫猫形象", "Choose a cat persona")}
            >
              {(() => {
                const usedBy = usedCatAvatarOwners(existingProfiles, value.id);
                return CAT_PRESETS.map((cat) => {
                  const owner = usedBy.get(cat.value);
                  const selected = value.avatar === cat.value;
                  const breedLabel = t(cat.zhName, cat.enName);
                  return (
                    <button
                      key={cat.id}
                      type="button"
                      className={cx("cat-option")}
                      aria-pressed={selected}
                      aria-label={
                        owner
                          ? t(
                              `${breedLabel}（已被 ${owner} 使用）`,
                              `${breedLabel} (used by ${owner})`,
                            )
                          : breedLabel
                      }
                      disabled={!!owner}
                      title={
                        owner
                          ? t(
                              `已被 ${owner} 使用`,
                              `Used by ${owner}`,
                            )
                          : `${breedLabel} · ${t(cat.zhTag, cat.enTag)}`
                      }
                      onClick={() => {
                        setAvatarError("");
                        update({ avatar: cat.value });
                      }}
                    >
                      <span className={cx("cat-option-portrait")}>
                        <CatPortrait preset={cat} size={40} />
                      </span>
                      <span className={cx("cat-option-name")}>{breedLabel}</span>
                      <span className={cx("cat-option-meta")}>
                        {owner
                          ? t(`已被 ${owner} 使用`, `Used: ${owner}`)
                          : t(cat.zhTag, cat.enTag)}
                      </span>
                    </button>
                  );
                });
              })()}
            </div>
          </div>
          <div className={cx("avatar-field")}>
            <span className={cx("avatar-preview")}>
              <Avatar
                name={value.display_name || t("新成员", "New member")}
                avatar={value.avatar}
                executor={value.executor}
              />
            </span>
            <div className={cx("avatar-controls")}>
              <div className={cx("avatar-actions")}>
                <label className={cx("avatar-upload")}>
                  {value.avatar
                    ? t("替换头像", "Replace avatar")
                    : t("上传头像", "Upload avatar")}
                  <input
                    className={cx("sr-only")}
                    type="file"
                    accept="image/png,image/jpeg"
                    aria-label={t("上传头像", "Upload avatar")}
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
                    {t("移除头像", "Remove avatar")}
                  </button>
                )}
              </div>
              <small>
                {t(
                  "也可上传自定义 PNG/JPEG（最大 512 KiB、2048×2048 像素）；留空显示名称首字。",
                  "Or upload a custom PNG/JPEG (up to 512 KiB and 2048×2048 px); leave blank for initials.",
                )}
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
          <h3>{t("执行与模型", "CLI & model")}</h3>
          <label htmlFor={field("executor")}>
            {t("执行 CLI", "Execution CLI")}
          </label>
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
            <option value="">
              {t("请选择本机 CLI", "Select a local CLI")}
            </option>
            {executors.map((id) => {
              const info = system.executors?.find((item) => item.id === id);
              return (
                <option key={id} value={id}>
                  {id} ·{" "}
                  {!info?.installed
                    ? t("未检测到", "Not detected")
                    : info.supported === false
                      ? t("当前版本不支持", "Unsupported version")
                      : t("已安装", "Installed")}
                </option>
              );
            })}
          </Select>
          {!value.executor && (
            <p role="status" className={cx("member-note")}>
              {t(
                "此成员尚未绑定本机 CLI。请选择执行 CLI；原 API 模型配置不会自动沿用。",
                "This member is not bound to a local CLI yet. Please select an execution CLI.",
              )}
            </p>
          )}
          {executorInfo?.reason && (
            <p role="status" className={cx("member-note")}>
              {executorInfo.supported === false
                ? t("此 CLI 当前不可执行：", "This CLI cannot be run: ")
                : t("说明：", "Note: ")}
              {executorInfo.reason}
            </p>
          )}
          <label htmlFor={field("model")}>
            {t("模型（留空使用 CLI 默认）", "Model (leave blank for CLI default)")}
          </label>
          <div className={cx("verify-row")}>
            <input
              id={field("model")}
              value={value.model === "default" ? "" : value.model}
              placeholder={
                value.executor === "opencode"
                  ? t(
                      "provider/model 或 provider/model#variant",
                      "provider/model or provider/model#variant",
                    )
                  : t("CLI 默认模型", "CLI default model")
              }
              onChange={(event) =>
                update({
                  model: event.target.value || "default",
                  ...(openCode ? { thinking_level: "" } : {}),
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
              {verifying
                ? t("验证中…", "Verifying…")
                : t("验证模型连接", "Verify connection")}
            </button>
          </div>
          <small>
            {t(
              "验证会复用本机 CLI 的供应商与登录配置，不需要额外登录或密钥；模型留空时按 CLI 默认模型验证。",
              "Verification reuses your local CLI's provider and login configuration without extra keys; leaving model blank verifies the CLI's default model.",
            )}
          </small>
          <label htmlFor={field("context")}>
            {t("上下文预算（tokens）", "Context budget (tokens)")}
          </label>
          <input
            id={field("context")}
            type="number"
            min={minContextTokens}
            step={1}
            inputMode="numeric"
            value={contextSize}
            placeholder={
              value.executor
                ? t(
                    `跟随 ${value.executor} 默认`,
                    `Follow ${value.executor} default`,
                  )
                : t("跟随执行 CLI 默认", "Follow CLI default")
            }
            aria-describedby={field("context-hint")}
            onChange={(event) => {
              setSaved(false);
              setContextSize(event.target.value);
            }}
          />
          <small id={field("context-hint")}>
            {t(
              "留空时由所选 CLI 按其默认上下文窗口处理；填写 token 数后，会按该值限制发给此成员的单次输入大小。",
              "Leave blank to use the CLI's default context window; enter a token count to cap the initial prompt size sent to this member.",
            )}
          </small>
          <label htmlFor={field("thinking")}>
            {t("思考等级（留空保持 CLI 默认）", "Thinking level (leave blank for CLI default)")}
          </label>
          <Select
            id={field("thinking")}
            value={value.thinking_level ?? ""}
            disabled={
              !value.executor ||
              (!levels.length && !value.thinking_level) ||
              geminiNeedsModel ||
              variantLoading
            }
            onChange={(thinking) => update({ thinking_level: thinking })}
          >
            <option value="">{t("保持 CLI 默认", "Keep CLI default")}</option>
            {openCode &&
              value.thinking_level &&
              !levels.includes(value.thinking_level) && (
                <option value={value.thinking_level}>
                  {value.thinking_level}
                  {t("（待校验）", " (unverified)")}
                </option>
              )}
            {levels.map((level) => (
              <option key={level} value={level}>
                {level}
              </option>
            ))}
          </Select>
          {geminiNeedsModel && (
            <small>
              {t(
                "Gemini 选择思考等级前必须填写明确模型，不能留空使用默认。",
                "Specify an explicit model before choosing a thinking level for Gemini.",
              )}
            </small>
          )}
          <small>
            {value.executor === "opencode"
              ? variantLoading
                ? t(
                    "正在读取当前模型支持的思考级别…",
                    "Loading supported thinking levels for this model…",
                  )
                : variantError
                  ? t(
                      `无法读取思考级别：${variantError}`,
                      `Could not load thinking levels: ${variantError}`,
                    )
                  : levels.length
                    ? t(
                        "按当前模型提供的变体选择，留空沿用本机或模型标识中的配置。",
                        "Choose from variants supported by this model, or leave blank to use your local configuration.",
                      )
                    : t(
                        "当前模型未声明可选思考变体，沿用本机配置。",
                        "This model declares no thinking variants; using local configuration.",
                      )
              : t(
                  "留空时沿用 CLI 已有的思考等级配置。",
                  "Leave blank to keep the CLI's existing thinking level setting.",
                )}
          </small>
          {verified && (
            <p role="status" className={cx("verify-ok")}>
              {t(
                "已验证当前执行 CLI、模型、上下文预算（tokens）和思考等级。",
                "Verified the current CLI, model, context budget, and thinking level.",
              )}
            </p>
          )}
          {verifyError && (
            <p role="alert" className={cx("error")}>
              {verifyError}
            </p>
          )}
        </section>
        <section className={cx("member-section")}>
          <legend>
            {catalog ? t("启用状态", "Status") : t("成员状态", "Member status")}
          </legend>
          <label className={cx("check-label")}>
            <input
              type="checkbox"
              checked={value.disabled}
              onChange={(event) => update({ disabled: event.target.checked })}
            />
            {catalog
              ? t("在所有工作空间停用此成员", "Disable this member across all projects")
              : t("停用此成员", "Disable this member")}
          </label>
          <small>
            {t(
              "工具授权由全局权限模式统一决定，不再按成员配置；停用后该成员不参与任何工作空间的执行。",
              "Tool permissions follow the global permission mode; when disabled, this member will not participate in runs in any project.",
            )}
          </small>
        </section>
        {error && (
          <p className={cx("error")} role="alert">
            {error}
          </p>
        )}
        {saved && (
          <p role="status">
            {t("已保存，可以继续编辑此成员。", "Saved. You can continue editing this member.")}
          </p>
        )}
        <div className={cx("form-actions")}>
          <button className={cx("primary")} disabled={saved || saving}>
            {saving
              ? t("保存中…", "Saving…")
              : t("保存成员", "Save member")}
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
  save: (value: { enabled: boolean }) => Promise<void>;
  unlink: () => Promise<void>;
}) {
  const [enabled, setEnabled] = useState(membership?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
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
        submit(() => save({ enabled }));
      }}
    >
      <fieldset disabled={busy}>
        <legend>{t("本工作空间成员", "Project membership")}</legend>
        <label className={cx("check-label")}>
          <input
            type="checkbox"
            checked={enabled}
            onChange={(event) => {
              setEnabled(event.target.checked);
              setSaved(false);
            }}
          />
          {t("在本工作空间启用", "Enabled in this project")}
        </label>
      </fieldset>
      <p className={cx("member-note")}>
        {agent.disabled
          ? t(
              "该成员已在全局停用，加入本空间也不会执行。",
              "This member is globally disabled and will not run even if added to this project.",
            )
          : t(
              "成员的工具与命令边界由设置中的全局权限决定；此处只管理是否加入与是否启用。",
              "Tool and command boundaries follow the global permission mode; manage project membership and enabled state here.",
            )}
        {t(
          "移出仅撤销本空间访问，保留全局档案与历史记录。",
          " Removing only unlinks this project while preserving the global profile and history.",
        )}
      </p>
      {error && (
        <p role="alert" className={cx("error")}>
          {error}
        </p>
      )}
      {saved && (
        <p role="status">
          {t("本空间成员设置已保存。", "Project member settings saved.")}
        </p>
      )}
      <div className={cx("form-actions")}>
        {membership && (
          <button type="button" disabled={busy} onClick={() => submit(unlink)}>
            {t("移出本工作空间", "Remove from project")}
          </button>
        )}
        <button className={cx("primary")} disabled={busy}>
          {busy
            ? t("保存中…", "Saving…")
            : membership
              ? t("保存本空间设置", "Save project settings")
              : t("加入本工作空间", "Add to project")}
        </button>
      </div>
    </form>
  );
}
