import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import type {
  Agent,
  Run,
  Settings,
  SettingsInput,
  System,
  Thread,
} from "../api/client";
import { api, statusText } from "../api/client";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import layout from "../styles/layout.module.css";
import conversation from "../styles/conversation.module.css";
import panels from "../styles/panels.module.css";
import settings from "../styles/settings.module.css";
import {
  errorText,
  executionPolicyRows,
  formatDateTime,
  getLegacyPolicyNote,
  getPolicyNote,
  limitLabel,
  runMethod,
  skillLabel,
} from "../lib/presentation";
import { type Language, t, useLanguage } from "../lib/i18n";
import { MembersSettings } from "./forms/MembersSettings";
import {
  CollaborationBudget,
  PermissionSettings,
} from "./PermissionSettings";
import type { ExecutionMode } from "../app/useGlobalSettings";
import { AppearanceSelect, type Appearance } from "./AppearanceSelect";
import { Icon, type IconName } from "./Icon";
import { Modal } from "./Modal";
import { Select } from "./Select";

const cx = scoped(base, controls, layout, conversation, panels, settings);

type SettingsTab =
  | "appearance"
  | "members"
  | "collaboration"
  | "permission"
  | "service"
  | "trash";
type TrashItem = { thread: Thread; workspace_name: string };

/** Only collaboration defaults; the permission is patched from its own page. */
const editable = (settings: Settings): SettingsInput => ({
  default_coordinator_id: settings.default_coordinator_id ?? "",
  collaboration_budget: {
    auto_continuations: settings.collaboration_budget?.auto_continuations ?? 20,
    parallel_branches: settings.collaboration_budget?.parallel_branches ?? 4,
    writer_branches: settings.collaboration_budget?.writer_branches ?? 2,
    fix_rounds: settings.collaboration_budget?.fix_rounds ?? 3,
    no_progress_failures:
      settings.collaboration_budget?.no_progress_failures ?? 2,
  },
});

export function SettingsDialog({
  system,
  setSystem,
  close,
  lock,
  settings,
  saveSettings,
  agents = [],
  saveAgent,
  deleteAgent,
  onTrashChanged,
  appearance,
  onAppearance,
  executionMode,
  onExecutionModeChange,
  permissionLoading,
  permissionError,
  permissionThreadID,
  permissionThreadTitle,
}: {
  system: System;
  setSystem: (value: System) => void;
  close: () => void;
  lock: () => Promise<void>;
  settings?: Settings;
  saveSettings?: (value: SettingsInput) => Promise<Settings>;
  agents?: Agent[];
  saveAgent?: (value: Agent) => Promise<Agent>;
  deleteAgent?: (value: Agent) => Promise<void>;
  onTrashChanged?: () => Promise<void> | void;
  appearance?: Appearance;
  onAppearance?: (value: Appearance) => void;
  /** The global choice; omitted hides the permission category. */
  executionMode?: ExecutionMode;
  onExecutionModeChange?: (mode: ExecutionMode) => Promise<unknown>;
  permissionLoading?: boolean;
  permissionError?: string;
  permissionThreadID?: string;
  permissionThreadTitle?: string;
}) {
  useLanguage();
  const tabs: [SettingsTab, string, IconName][] = [];
  if (saveAgent) tabs.push(["members", t("成员", "Members"), "users"]);
  if (executionMode && onExecutionModeChange)
    tabs.push(["permission", t("权限", "Permissions"), "tools"]);
  if (settings && saveSettings)
    tabs.push(["collaboration", t("协作", "Collaboration"), "layers"]);
  if (appearance && onAppearance)
    tabs.push(["appearance", t("外观", "Appearance"), "appearance"]);
  tabs.push(["service", t("服务", "Service"), "settings"]);
  tabs.push(["trash", t("回收站", "Trash"), "trash"]);
  const [tab, setTab] = useState<SettingsTab>(tabs[0][0]);
  const tabRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const bodyRef = useRef<HTMLDivElement>(null);
  const selectTab = (next: SettingsTab) => {
    setTab(next);
    bodyRef.current?.scrollTo?.({ top: 0 });
  };
  const move = (
    event: ReactKeyboardEvent<HTMLButtonElement>,
    index: number,
  ) => {
    const keys = [
      "ArrowDown",
      "ArrowRight",
      "ArrowUp",
      "ArrowLeft",
      "Home",
      "End",
    ];
    if (!keys.includes(event.key)) return;
    event.preventDefault();
    const next =
      event.key === "Home"
        ? 0
        : event.key === "End"
          ? tabs.length - 1
          : (index +
              (event.key === "ArrowUp" || event.key === "ArrowLeft" ? -1 : 1) +
              tabs.length) %
            tabs.length;
    selectTab(tabs[next][0]);
    tabRefs.current[next]?.focus();
  };

  return (
    <Modal title={t("设置", "Settings")} close={close} wide flush>
      <div className={cx("settings-container")}>
        <div
          className={cx("settings-tabs")}
          role="tablist"
          aria-label={t("设置分类", "Settings categories")}
          aria-orientation="vertical"
        >
          {tabs.map(([key, label, icon], index) => (
            <button
              key={key}
              ref={(node) => {
                tabRefs.current[index] = node;
              }}
              type="button"
              role="tab"
              id={`settings-tab-${key}`}
              className={cx(`settings-tab ${tab === key ? "active" : ""}`)}
              aria-selected={tab === key}
              aria-controls={`settings-panel-${key}`}
              tabIndex={tab === key ? 0 : -1}
              onClick={() => selectTab(key)}
              onKeyDown={(event) => move(event, index)}
            >
              <Icon name={icon} size={15} />
              {label}
            </button>
          ))}
        </div>
        <div ref={bodyRef} className={cx("settings-body")}>
          {tabs.map(([key]) => (
            <div
              key={key}
              className={cx("settings-panel")}
              id={`settings-panel-${key}`}
              role="tabpanel"
              aria-labelledby={`settings-tab-${key}`}
              tabIndex={tab === key ? 0 : -1}
              hidden={tab !== key}
            >
              {tab === key && key === "members" && saveAgent && (
                <MembersSettings
                  embedded
                  global
                  agents={[]}
                  system={system}
                  workspaceID=""
                  refresh={async () => {}}
                  close={close}
                  save={saveAgent}
                  remove={deleteAgent}
                />
              )}
              {tab === key &&
                key === "appearance" &&
                appearance &&
                onAppearance && (
                  <AppearanceSettings
                    appearance={appearance}
                    onChange={onAppearance}
                  />
                )}
              {tab === key &&
                key === "permission" &&
                executionMode &&
                onExecutionModeChange && (
                  <PermissionSettings
                    settings={settings}
                    onExecutionMode={onExecutionModeChange}
                    loading={permissionLoading}
                    error={permissionError}
                    threadID={permissionThreadID}
                    threadTitle={permissionThreadTitle}
                  />
                )}
              {tab === key &&
                key === "collaboration" &&
                settings &&
                saveSettings && (
                  <CollaborationSettings
                    agents={agents}
                    settings={settings}
                    save={saveSettings}
                  />
                )}
              {tab === key && key === "service" && (
                <ServiceSettings
                  system={system}
                  setSystem={setSystem}
                  lock={lock}
                />
              )}
              {tab === key && key === "trash" && (
                <TrashSettings
                  settings={settings}
                  saveSettings={saveSettings}
                  onTrashChanged={onTrashChanged}
                />
              )}
            </div>
          ))}
        </div>
      </div>
    </Modal>
  );
}

/** Appearance is a local preference and never changes model permissions. */
function AppearanceSettings({
  appearance,
  onChange,
}: {
  appearance: Appearance;
  onChange: (value: Appearance) => void;
}) {
  const [language, setLanguage] = useLanguage();
  return (
    <section aria-labelledby="settings-appearance">
      <h2 id="settings-appearance">{t("外观", "Appearance")}</h2>
      <p className={cx("muted")}>
        {t(
          "这些偏好仅保存在当前浏览器中，可随时切换界面语言与主题模式。",
          "These preferences are saved in your current browser. Switch language or theme anytime.",
        )}
      </p>
      <label className={cx("settings-appearance-select")}>
        {t("外观模式", "Appearance mode")}
        <AppearanceSelect value={appearance} onChange={onChange} />
      </label>
      <label className={cx("settings-appearance-select")}>
        {t("界面语言", "Language")}
        <Select
          aria-label={t("界面语言", "Language")}
          value={language}
          onChange={(selected) => setLanguage(selected as Language)}
        >
          <option value="zh-CN">简体中文</option>
          <option value="en">English</option>
        </Select>
      </label>
    </section>
  );
}

/** Global collaboration defaults; a member is only used where the workspace grants it. */
function CollaborationSettings({
  agents,
  settings,
  save,
}: {
  agents: Agent[];
  settings: Settings;
  save: (value: SettingsInput) => Promise<Settings>;
}) {
  useLanguage();
  const [value, setValue] = useState<SettingsInput>(() => editable(settings));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const enabled = agents.filter((agent) => !agent.disabled);
  // Saving never resends a member the server would reject: disabled or removed
  // profiles are cleared from the value before the PATCH.
  const clearUnavailable = (current: SettingsInput): SettingsInput => {
    const ids = new Set(enabled.map((agent) => agent.id));
    return {
      default_coordinator_id: ids.has(current.default_coordinator_id ?? "")
        ? (current.default_coordinator_id ?? "")
        : "",
      collaboration_budget: current.collaboration_budget,
    };
  };
  const memberOptions = (current: string) => [
    <option key="" value="">
      {t("使用会话可用成员", "Use available conversation member")}
    </option>,
    ...enabled.map((agent) => (
      <option key={agent.id} value={agent.id}>
        {agent.display_name} (@{agent.handle})
      </option>
    )),
    ...(current && !enabled.some((agent) => agent.id === current)
      ? [
          <option key="unavailable" value={current}>
            {t(
              "原成员已停用或不存在，保存时清除",
              "Previous member is disabled or missing (cleared on save)",
            )}
          </option>,
        ]
      : []),
  ];
  return (
    <>
      <form
        className={cx("settings-form")}
        onSubmit={(event) => {
          event.preventDefault();
          setSaving(true);
          setError("");
          setSaved(false);
          void save(clearUnavailable(value))
            .then((next) => {
              setValue(editable(next));
              setSaved(true);
            })
            .catch((error) => setError(errorText(error)))
            .finally(() => setSaving(false));
        }}
      >
        <section className={cx("settings-section")}>
          <h3>{t("默认分工", "Default coordination")}</h3>
          <p>
            {t(
              "新建会话和安排任务时默认由该成员协调（直接 @ 指定成员时以你的指定为准）。若默认协调者不在当前空间或已停用，会自动改用当前会话中的可用成员。",
              "Used when creating conversations and coordinating tasks (explicit @mentions always take precedence). If the default coordinator is not in the current project or is disabled, an available member in the conversation is used instead.",
            )}
          </p>
          <label>
            {t("默认协调者", "Default coordinator")}
            <Select
              value={value.default_coordinator_id ?? ""}
              onChange={(selected) => {
                setSaved(false);
                setValue({ ...value, default_coordinator_id: selected });
              }}
            >
              {memberOptions(value.default_coordinator_id ?? "")}
            </Select>
          </label>
        </section>
        <section className={cx("settings-section")}>
          <h3>{t("协作方式", "How collaboration works")}</h3>
          <p>
            {t(
              "所有会话统一采用单一动态协作内核：你用 @ 指定的成员会直接执行，未指定时由协调者按需安排分工，无需手动选择固定流程。",
              "All conversations run on a single dynamic collaboration kernel: members you @mention execute directly, and otherwise the coordinator assigns work as needed without rigid preset workflows.",
            )}
          </p>
          {error && (
            <p role="alert" className={cx("error")}>
              {error}
            </p>
          )}
          {saved && (
            <p role="status">
              {t(
                "协作默认已保存，新会话会使用这些选择。",
                "Collaboration defaults saved; new conversations will use these settings.",
              )}
            </p>
          )}
          <div className={cx("form-actions")}>
            <button className={cx("primary")} disabled={saving}>
              {saving
                ? t("保存中…", "Saving…")
                : t("保存协作默认", "Save collaboration defaults")}
            </button>
          </div>
        </section>
      </form>
      <CollaborationBudget
        settings={settings}
        save={(policy) =>
          // Field merge: only the budget is sent, so the permission, roles and
          // retention are never rewritten by saving the collaboration defaults.
          save({ collaboration_budget: policy })
        }
      />
    </>
  );
}

function ServiceSettings({
  system,
  setSystem,
  lock,
}: {
  system: System;
  setSystem: (value: System) => void;
  lock: () => Promise<void>;
}) {
  useLanguage();
  const [executor, setExecutor] = useState(
    system.executors?.find((item) => item.installed && item.supported !== false)
      ?.id ?? "claude",
  );
  const [model, setModel] = useState("");
  const [verifying, setVerifying] = useState(false);
  const [error, setError] = useState("");
  const key = `${executor}:${model.trim() || "default"}`;
  const runnable = system.executors?.some(
    (item) =>
      item.id === executor && item.installed && item.supported !== false,
  );
  return (
    <>
      <section className={cx("settings-section")}>
        <h3>{t("服务状态", "Service status")}</h3>
        <p>
          {t(
            "成员直接调用本机安装的 Claude、Codex、Gemini、Pi 或 OpenCode CLI，复用你本机已配置好的模型与登录认证，网页端不会读取或保存你的密钥。",
            "Members invoke your locally installed Claude, Codex, Gemini, Pi, or OpenCode CLI, reusing your local model, provider, and auth configuration. The web UI never reads or stores your credentials.",
          )}
        </p>
        <div className={cx("settings-grid")}>
          <div className={cx("settings-card")}>
            <h4>{t("执行限制", "Execution limits")}</h4>
            <ul>
              {Object.entries(system.limits ?? {})
                .map(
                  ([name, value]) => [name, limitLabel(name, value)] as const,
                )
                .filter(([, label]) => label)
                .map(([name, label]) => (
                  <li key={name}>{label}</li>
                ))}
              <li>
                {t(
                  "同一时间处理一轮协作，其余按顺序排队；可停止当前协作。",
                  "Processes one collaboration round at a time and queues additional runs in order; active runs can be stopped anytime.",
                )}
              </li>
            </ul>
          </div>
          <div className={cx("settings-card")}>
            <h4>{t("本机 CLI", "Local CLIs")}</h4>
            <div className={cx("service-executors")}>
              {system.executors?.map((item) => {
                const runnable = item.installed && item.supported !== false;
                return (
                  <div key={item.id} className={cx("service-executor")}>
                    <div className={cx("service-executor-head")}>
                      <strong>{item.id}</strong>
                      <span
                        className={cx(
                          `service-badge ${runnable ? "runnable" : "unavailable"}`,
                        )}
                      >
                        {runnable
                          ? t("可执行", "Ready")
                          : item.installed
                            ? t("不可执行", "Not runnable")
                            : t("未安装", "Not installed")}
                      </span>
                    </div>
                    <p>
                      {item.installed
                        ? t(
                            `已安装 · ${item.version || "版本未知"}`,
                            `Installed · ${item.version || "Unknown version"}`,
                          )
                        : t("未检测到可执行文件", "Executable not found")}
                      {item.adapter
                        ? t(
                            ` · 调用方式 ${item.adapter}`,
                            ` · Adapter ${item.adapter}`,
                          )
                        : ""}
                    </p>
                    <p className={cx("muted")}>
                      {item.installed
                        ? t(
                            `工具隔离 ${
                              item.supported === false
                                ? "不可确认"
                                : item.isolation === "enforced"
                                  ? "探测通过"
                                  : item.isolation === "candidate"
                                    ? "待验证（首次执行前自动探测）"
                                    : "待确认"
                            }`,
                            `Tool isolation: ${
                              item.supported === false
                                ? "unverifiable"
                                : item.isolation === "enforced"
                                  ? "verified"
                                  : item.isolation === "candidate"
                                    ? "probed on first run"
                                    : "pending"
                            }`,
                          )
                        : t(
                            "安装后才可探测工具隔离",
                            "Install to check tool isolation",
                          )}
                      {" · "}
                      {item.protocol === "verified"
                        ? t("连接与模型已验证", "Connection & model verified")
                        : item.installed
                          ? t(
                              "连接未验证（运行时再检查协议）",
                              "Connection unverified (checked at runtime)",
                            )
                          : t("尚未连接", "Not connected")}
                    </p>
                    {item.reason && (
                      <p className={cx("muted")}>
                        {item.supported === false
                          ? t("不支持", "Unsupported")
                          : t("说明", "Note")}
                        {t("：", ": ")}
                        {item.reason}
                      </p>
                    )}
                  </div>
                );
              })}
              {!system.executors?.length && (
                <p className={cx("muted")}>
                  {t("未检测到本机 CLI。", "No local CLIs detected.")}
                </p>
              )}
            </div>
          </div>
          <div className={cx("settings-card")}>
            <h4>{t("在线搜索", "Web search")}</h4>
            <p>
              {statusText[system.search_status] ??
                system.search_status ??
                t("未配置", "Not configured")}
              {system.search_provider ? ` · ${system.search_provider}` : ""}
            </p>
            <p className={cx("muted")}>
              {system.search_status === "configured"
                ? t(
                    "搜索查询会通过网络发送给搜索服务。",
                    "Search queries are sent over the network to the configured search provider.",
                  )
                : t(
                    "搜索不可用时仍可读取公开 URL 和本地资料。",
                    "When search is unavailable, members can still read public URLs and local files.",
                  )}
            </p>
          </div>
          <div className={cx("settings-card")}>
            <h4>{t("资料目录范围", "Allowed root directories")}</h4>
            <pre className={cx("pre")}>
              {system.roots.join("\n") ||
                t("在各工作空间设置中指定", "Configured per project")}
            </pre>
          </div>
        </div>
        <p className={cx("muted")}>
          {t(
            "实际执行时会通过 CLI 将问题与相关片段发送给模型服务；已安装不代表连接或模型可用，可在下方验证。",
            "During execution, prompts and relevant file snippets are sent to the model provider via the CLI. Installed does not guarantee an active connection or model.",
          )}
        </p>
      </section>
      <section className={cx("settings-section")}>
        <h3>{t("验证连接", "Verify connection")}</h3>
        <p>
          {t(
            "测试所选 CLI 与模型能否正常连通；模型留空时使用该 CLI 的默认模型。验证会发起一次极短请求，可能产生少量用量。",
            "Test whether the selected CLI and model can connect; leave model blank to use the CLI's default model. Verification sends a short test request and may use a small number of tokens.",
          )}
        </p>
        <form
          className={cx("settings-form")}
          onSubmit={(event) => {
            event.preventDefault();
            setVerifying(true);
            setError("");
            void api("/api/v1/system/verify", "POST", {
              executor,
              model: model.trim() || "default",
            })
              .then(() => api<System>("/api/v1/system"))
              .then(setSystem)
              .catch((error) => setError(errorText(error)))
              .finally(() => setVerifying(false));
          }}
        >
          <div className={cx("settings-fields")}>
            <label>
              {t("验证 CLI", "CLI to verify")}
              <Select
                value={executor}
                onChange={(selected) => {
                  setExecutor(selected);
                  setError("");
                }}
              >
                {(system.executors?.length
                  ? system.executors.map((item) => item.id)
                  : ["claude", "codex", "gemini", "pi", "opencode"]
                ).map((id) => (
                  <option key={id} value={id}>
                    {id}
                  </option>
                ))}
              </Select>
            </label>
            <label>
              {t(
                "验证模型（留空使用 CLI 默认）",
                "Model to verify (leave blank for CLI default)",
              )}
              <input
                value={model}
                onChange={(event) => {
                  setModel(event.target.value);
                  setError("");
                }}
                placeholder={t("CLI 默认模型", "CLI default model")}
              />
            </label>
          </div>
          {error && (
            <p role="alert" className={cx("error")}>
              {error}
            </p>
          )}
          {system.verified[key] && (
            <p role="status">
              {t(
                `验证时间：${formatDateTime(system.verified[key])} · 该 CLI 与模型上次已验证`,
                `Verified at: ${formatDateTime(system.verified[key])} · CLI and model verified`,
              )}
            </p>
          )}
          <div className={cx("form-actions")}>
            <button className={cx("primary")} disabled={verifying || !runnable}>
              {verifying
                ? t("正在验证…", "Verifying…")
                : t(
                    "验证模型连接（可能产生用量）",
                    "Verify model connection (may use tokens)",
                  )}
            </button>
          </div>
        </form>
      </section>
      <section className={cx("settings-section")}>
        <h3>{t("锁定", "Lock")}</h3>
        <div className={cx("settings-security")}>
          <p className={cx("muted")}>
            {t(
              "离开时可锁定页面，正在进行的协作会继续运行。",
              "Lock the UI while away; active collaboration runs will continue in the background.",
            )}
          </p>
          <button
            type="button"
            onClick={() => {
              void lock().catch((error) => setError(errorText(error)));
            }}
          >
            {t("锁定工作空间", "Lock workspace")}
          </button>
        </div>
      </section>
    </>
  );
}

function TrashSettings({
  settings,
  saveSettings,
  onTrashChanged,
}: {
  settings?: Settings;
  saveSettings?: (value: SettingsInput) => Promise<Settings>;
  onTrashChanged?: () => Promise<void> | void;
}) {
  useLanguage();
  const [items, setItems] = useState<TrashItem[]>();
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [confirm, setConfirm] = useState<string[]>();
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [days, setDays] = useState(String(settings?.retention_days ?? 7));
  const [savingDays, setSavingDays] = useState(false);
  const [savedDays, setSavedDays] = useState(false);
  const selectAll = useRef<HTMLInputElement>(null);
  const running = useRef(false);
  const selectedItems = (items ?? []).filter((item) =>
    selected.has(item.thread.id),
  );
  const selectedCount = selectedItems.length;
  const allSelected = !!items?.length && selectedCount === items.length;
  const load = useCallback(async () => {
    setLoading(true);
    setLoadError("");
    try {
      const result = await api<{ items: TrashItem[] | null }>("/api/v1/trash");
      const next = result.items ?? [];
      setItems(next);
      setSelected(
        (current) =>
          new Set(
            [...current].filter((id) =>
              next.some((item) => item.thread.id === id),
            ),
          ),
      );
      setConfirm(undefined);
    } catch (error) {
      setLoadError(errorText(error));
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);
  useEffect(() => {
    setDays(String(settings?.retention_days ?? 7));
  }, [settings?.revision, settings?.retention_days]);
  useEffect(() => {
    if (selectAll.current)
      selectAll.current.indeterminate = selectedCount > 0 && !allSelected;
  }, [selectedCount, allSelected]);

  const toggleAll = (checked: boolean) => {
    setConfirm(undefined);
    setNotice("");
    setSelected(
      checked
        ? new Set((items ?? []).map((item) => item.thread.id))
        : new Set(),
    );
  };
  const toggleItem = (id: string, checked: boolean) => {
    setConfirm(undefined);
    setNotice("");
    setSelected((current) => {
      const next = new Set(current);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  };
  // Each item keeps its own If-Match check, so a batch never bypasses the
  // optimistic concurrency rule of a single restore or purge.
  const process = async (
    kind: "restore" | "permanent",
    targets: TrashItem[],
  ) => {
    if (!targets.length || running.current)
      return { completed: [] as TrashItem[], failures: 0 };
    running.current = true;
    setBusy(kind);
    setError("");
    setNotice("");
    const completed: TrashItem[] = [];
    const failures: string[] = [];
    try {
      for (const item of targets) {
        try {
          await api(
            kind === "restore"
              ? `/api/v1/threads/${item.thread.id}/restore`
              : `/api/v1/threads/${item.thread.id}/permanent`,
            kind === "restore" ? "POST" : "DELETE",
            kind === "restore" ? {} : undefined,
            { "If-Match": String(item.thread.revision) },
          );
          completed.push(item);
        } catch (failure) {
          failures.push(`${item.thread.title}：${errorText(failure)}`);
        }
      }
      if (completed.length) {
        const done = new Set(completed.map((item) => item.thread.id));
        setItems((current) =>
          (current ?? []).filter((item) => !done.has(item.thread.id)),
        );
        setSelected(
          (current) => new Set([...current].filter((id) => !done.has(id))),
        );
      }
      setConfirm(undefined);
      if (failures.length) setError(failures.join(t("；", "; ")));
      return { completed, failures: failures.length };
    } finally {
      running.current = false;
      setBusy("");
    }
  };
  const restoreSelected = async () => {
    const result = await process("restore", selectedItems);
    if (!result.completed.length) return;
    try {
      await onTrashChanged?.();
      setNotice(
        t(
          `已恢复 ${result.completed.length} 个会话。`,
          `Restored ${result.completed.length} conversation(s).`,
        ),
      );
    } catch (failure) {
      setError(errorText(failure));
    }
  };
  const purgeSelected = async () => {
    const ids = confirm ?? [];
    const targets = (items ?? []).filter((item) =>
      ids.includes(item.thread.id),
    );
    const result = await process("permanent", targets);
    if (!result.completed.length) return;
    try {
      await onTrashChanged?.();
      setNotice(
        t(
          `已永久删除 ${result.completed.length} 个会话。`,
          `Permanently deleted ${result.completed.length} conversation(s).`,
        ),
      );
    } catch (failure) {
      setError(errorText(failure));
    }
  };

  return (
    <>
      {settings && saveSettings && (
        <section className={cx("settings-section")}>
          <h3>{t("保留周期", "Retention period")}</h3>
          <p className={cx("muted")}>
            {t(
              "保存后会同步重算回收站中会话的清理时间；之后删除的会话也使用新周期。",
              "Saving recalculates the auto-purge time for items currently in the trash; newly deleted conversations will also use this retention period.",
            )}
          </p>
          <form
            className={cx("settings-form settings-inline-form")}
            onSubmit={(event) => {
              event.preventDefault();
              const next = Number(days);
              if (!Number.isInteger(next) || next < 1 || next > 3650) {
                setError(
                  t(
                    "保留天数必须是 1 到 3650 之间的整数。",
                    "Retention days must be an integer between 1 and 3650.",
                  ),
                );
                return;
              }
              setSavingDays(true);
              setSavedDays(false);
              setError("");
              void saveSettings({
                retention_days: next,
              })
                .then(async (saved) => {
                  setDays(String(saved.retention_days ?? next));
                  await load();
                  setSavedDays(true);
                })
                .catch((failure) => setError(errorText(failure)))
                .finally(() => setSavingDays(false));
            }}
          >
            <label>
              {t("回收站保留天数", "Trash retention days")}
              <input
                type="number"
                min={1}
                max={3650}
                value={days}
                onChange={(event) => {
                  setSavedDays(false);
                  setDays(event.target.value);
                }}
              />
            </label>
            <button className={cx("primary")} disabled={savingDays}>
              {savingDays
                ? t("保存中…", "Saving…")
                : t("保存保留周期", "Save retention period")}
            </button>
            {savedDays && (
              <p role="status">
                {t(
                  "保留周期已保存，回收站中的清理时间已同步更新。",
                  "Retention period saved; trash cleanup times have been updated.",
                )}
              </p>
            )}
          </form>
        </section>
      )}
      <section className={cx("settings-section")}>
        <h3>{t("回收站", "Trash")}</h3>
        <p>
          {t(
            `删除的会话会在这里保留 ${settings?.retention_days ?? 7} 天。可多选或全选后批量恢复、永久删除；恢复后回到原工作空间。永久删除会立即移除消息、协作记录、来源和产物，无法恢复。`,
            `Deleted conversations are kept here for ${settings?.retention_days ?? 7} days. Select items to restore them to their original project or delete them permanently. Permanent deletion immediately removes messages, run logs, sources, and deliverables and cannot be undone.`,
          )}
        </p>
        {loading && (
          <p role="status" className={cx("muted")}>
            {t("正在读取回收站…", "Loading trash…")}
          </p>
        )}
        {loadError && (
          <p role="alert" className={cx("error")}>
            {loadError}
            <button
              type="button"
              className={cx("ghost small")}
              onClick={() => void load()}
            >
              {t("重试", "Retry")}
            </button>
          </p>
        )}
        {items && !loading && !loadError && !items.length && (
          <p className={cx("muted")}>{t("回收站为空。", "Trash is empty.")}</p>
        )}
        {!!items?.length && (
          <>
            <div className={cx("trash-toolbar")}>
              <label className={cx("check-label")}>
                <input
                  ref={selectAll}
                  type="checkbox"
                  checked={allSelected}
                  disabled={!!busy || loading}
                  onChange={(event) => toggleAll(event.target.checked)}
                />
                {t("全选", "Select all")}
              </label>
              <span className={cx("trash-count")}>
                {t(`已选 ${selectedCount} 项`, `${selectedCount} selected`)}
              </span>
              <div className={cx("trash-actions trash-bulk-actions")}>
                <button
                  type="button"
                  className={cx("ghost small")}
                  disabled={!selectedCount || !!busy || loading}
                  onClick={() => void restoreSelected()}
                >
                  {busy === "restore"
                    ? t("正在恢复…", "Restoring…")
                    : t("恢复所选", "Restore selected")}
                </button>
                <button
                  type="button"
                  className={cx("danger small")}
                  disabled={!selectedCount || !!busy || loading}
                  onClick={() => {
                    setError("");
                    setNotice("");
                    setConfirm(selectedItems.map((item) => item.thread.id));
                  }}
                >
                  {t("永久删除所选", "Delete permanently")}
                </button>
              </div>
            </div>
            {confirm && (
              <div
                className={cx("confirm-inline")}
                role="alertdialog"
                aria-label={t(
                  `确认永久删除 ${confirm.length} 个会话`,
                  `Confirm permanent deletion of ${confirm.length} conversation(s)`,
                )}
              >
                <p>
                  {t(
                    `永久删除已选的 ${confirm.length} 个会话？消息、协作记录、来源和产物会立即移除，无法恢复。`,
                    `Permanently delete ${confirm.length} selected conversation(s)? Messages, run logs, sources, and deliverables will be removed immediately and cannot be recovered.`,
                  )}
                </p>
                <div className={cx("form-actions")}>
                  <button
                    type="button"
                    disabled={!!busy}
                    onClick={() => setConfirm(undefined)}
                  >
                    {t("取消", "Cancel")}
                  </button>
                  <button
                    type="button"
                    className={cx("danger")}
                    disabled={!!busy}
                    onClick={() => void purgeSelected()}
                  >
                    {busy === "permanent"
                      ? t("正在处理…", "Deleting…")
                      : t("确认永久删除", "Confirm permanent delete")}
                  </button>
                </div>
              </div>
            )}
            <ul
              className={cx("trash-list")}
              aria-label={t("回收站会话", "Conversations in trash")}
            >
              {items.map((item) => (
                <li
                  key={item.thread.id}
                  className={cx(
                    `trash-item ${selected.has(item.thread.id) ? "selected" : ""}`,
                  )}
                >
                  <div className={cx("trash-row")}>
                    <input
                      type="checkbox"
                      className={cx("trash-check")}
                      aria-label={t(
                        `选择会话：${item.thread.title}`,
                        `Select conversation: ${item.thread.title}`,
                      )}
                      checked={selected.has(item.thread.id)}
                      disabled={!!busy || loading}
                      onChange={(event) =>
                        toggleItem(item.thread.id, event.target.checked)
                      }
                    />
                    <div className={cx("trash-main")}>
                      <strong>{item.thread.title}</strong>
                      <span className={cx("trash-meta")}>
                        <span>{item.workspace_name}</span>
                        <span>
                          {t(
                            `删除于 ${formatDateTime(item.thread.deleted_at) || "未知时间"}`,
                            `Deleted ${formatDateTime(item.thread.deleted_at) || "unknown time"}`,
                          )}
                        </span>
                        <span>
                          {t(
                            `计划于 ${formatDateTime(item.thread.purge_at) || "未知时间"} 自动清理`,
                            `Scheduled for auto-purge on ${formatDateTime(item.thread.purge_at) || "unknown time"}`,
                          )}
                        </span>
                      </span>
                    </div>
                  </div>
                </li>
              ))}
            </ul>
          </>
        )}
        {notice && (
          <p role="status" className={cx("muted")}>
            {notice}
          </p>
        )}
        {error && (
          <p role="alert" className={cx("error")}>
            {error}
          </p>
        )}
      </section>
    </>
  );
}

export function SnapshotDialog({
  run,
  close,
}: {
  run: Run;
  close: () => void;
}) {
  useLanguage();
  // Only the persisted method/selection reaches the snapshot; a missing record
  // stays the free label instead of being inferred from messages.
  const method = runMethod(run);
  const policyRows = executionPolicyRows(run.execution_policy);
  const startedAt = formatDateTime(run.started_at);
  const deadlineAt = formatDateTime(run.deadline_at);
  return (
    <Modal
      title={t("本次协作配置快照", "Collaboration Configuration Snapshot")}
      close={close}
    >
      <p>
        {t(
          `协作会话 · 创建于 ${formatDateTime(run.created_at)}`,
          `Collaborative conversation · Created ${formatDateTime(run.created_at)}`,
        )}
      </p>
      <p>
        {t(`本轮方法：${method.label}`, `Approach: ${method.label}`)}
      </p>
      {method.reason && (
        <p className={cx("muted")}>
          {t(`选择原因：${method.reason}`, `Reason: ${method.reason}`)}
        </p>
      )}
      {run.coordinator_id && (
        <p>
          {t("本轮协调者：", "Coordinator: ")}
          {run.agents?.find((agent) => agent.id === run.coordinator_id)
            ?.display_name || t("成员", "Member")}
        </p>
      )}
      <section className={cx("snapshot-member")}>
        <h3>
          {t("执行策略（本次协作冻结）", "Execution policy (locked for this run)")}
        </h3>
        {policyRows.length ? (
          <dl>
            {policyRows.map((row) => (
              <div key={row.label}>
                <dt>{row.label}</dt>
                <dd>{row.value}</dd>
              </div>
            ))}
          </dl>
        ) : (
          <p className={cx("muted")}>{getLegacyPolicyNote()}</p>
        )}
        {startedAt && (
          <p>{t(`开始时间：${startedAt}`, `Started at: ${startedAt}`)}</p>
        )}
        {run.execution_policy &&
          (run.execution_policy.run_timeout_ns > 0 ? (
            <p>
              {deadlineAt
                ? t(`截止时间：${deadlineAt}`, `Deadline: ${deadlineAt}`)
                : startedAt
                  ? t("截止时间：未记录", "Deadline: Not recorded")
                  : t(
                      "尚未派发，期限从首次执行开始计算",
                      "Not dispatched yet; time limit starts on first execution",
                    )}
            </p>
          ) : (
            <p>{t("截止时间：不设绝对期限", "Deadline: No hard time limit")}</p>
          ))}
        {!run.execution_policy && deadlineAt && (
          <p>{t(`截止时间：${deadlineAt}`, `Deadline: ${deadlineAt}`)}</p>
        )}
        {policyRows.length > 0 && (
          <p className={cx("muted")}>{getPolicyNote()}</p>
        )}
      </section>
      {run.agents?.map((agent) => (
        <section className={cx("snapshot-member")} key={agent.id}>
          <h3>
            {agent.display_name} (@{agent.handle})
          </h3>
          <p className={cx("muted")}>
            {t(`配置版本 ${agent.revision}`, `Config rev ${agent.revision}`)} ·{" "}
            {agent.executor ? `${agent.executor} · ` : ""}
            {agent.model === "default"
              ? t("CLI 默认模型", "CLI default model")
              : agent.model}
          </p>
          {agent.role_description && <p>{agent.role_description}</p>}
          <p className={cx("muted")}>
            {t(
              `可用工具：${agent.tools?.length ?? 0} 项`,
              `Available tools: ${agent.tools?.length ?? 0}`,
            )}
          </p>
        </section>
      ))}
      {run.skill_versions && (
        <section className={cx("snapshot-member")}>
          <h3>{t("共享协作技能", "Shared collaboration skills")}</h3>
          {Object.entries(run.skill_versions).map(([id, version]) => (
            <p key={id}>
              {skillLabel(id)} ·{" "}
              <code title={version}>{version.slice(0, 12)}</code>
            </p>
          ))}
          {!Object.keys(run.skill_versions).length && (
            <p className={cx("muted")}>
              {t(
                "本次协作未启用共享技能。",
                "No shared skills were enabled for this run.",
              )}
            </p>
          )}
        </section>
      )}
      <p className={cx("muted")}>
        {t(
          "历史快照不随成员改名、模型或权限编辑而改变。",
          "Historical snapshots remain unchanged even if member names, models, or permissions are edited later.",
        )}
      </p>
    </Modal>
  );
}
