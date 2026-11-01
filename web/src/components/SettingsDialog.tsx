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
  legacyPolicyNote,
  limitLabel,
  policyNote,
  roleLabel,
  runMethod,
  skillLabel,
  workflowRoles,
  workflows,
} from "../lib/presentation";
import { MembersSettings } from "./forms/MembersSettings";
import { Icon, type IconName } from "./Icon";
import { Modal } from "./Modal";
import { Select } from "./Select";

const cx = scoped(base, controls, layout, conversation, panels, settings);

type SettingsTab = "members" | "collaboration" | "service" | "trash";
type TrashItem = { thread: Thread; workspace_name: string };

const editable = (settings: Settings): SettingsInput => ({
  default_coordinator_id: settings.default_coordinator_id ?? "",
  assignments: Object.fromEntries(
    Object.entries(settings.assignments ?? {}).map(([workflow, roles]) => [
      workflow,
      { ...(roles ?? {}) },
    ]),
  ),
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
  onRestoreThread,
  onTrashChanged,
}: {
  system: System;
  setSystem: (value: System) => void;
  close: () => void;
  lock: () => Promise<void>;
  settings?: Settings;
  saveSettings?: (value: SettingsInput) => Promise<Settings>;
  agents?: Agent[];
  saveAgent?: (value: Agent) => Promise<Agent>;
  onRestoreThread?: (thread: Thread) => Promise<void> | void;
  onTrashChanged?: () => Promise<void> | void;
}) {
  const tabs: [SettingsTab, string, IconName][] = [];
  if (saveAgent) tabs.push(["members", "成员", "users"]);
  if (settings && saveSettings) tabs.push(["collaboration", "协作", "layers"]);
  tabs.push(["service", "服务", "settings"]);
  tabs.push(["trash", "回收站", "trash"]);
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
    <Modal title="设置" close={close} wide>
      <div className={cx("settings-container")}>
        <div
          className={cx("settings-tabs")}
          role="tablist"
          aria-label="设置分类"
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
                  onRestoreThread={onRestoreThread}
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
  const [value, setValue] = useState<SettingsInput>(() => editable(settings));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const enabled = agents.filter((agent) => !agent.disabled);
  const role = (workflow: string, name: string) =>
    value.assignments?.[workflow]?.[name] ?? "";
  const choose = (workflow: string, name: string, id: string) =>
    setValue((current) => ({
      ...current,
      assignments: {
        ...current.assignments,
        [workflow]: { ...current.assignments?.[workflow], [name]: id },
      },
    }));
  // Saving never resends a member the server would reject: disabled or removed
  // profiles are cleared from the value before the PATCH.
  const clearUnavailable = (current: SettingsInput): SettingsInput => {
    const ids = new Set(enabled.map((agent) => agent.id));
    return {
      default_coordinator_id: ids.has(current.default_coordinator_id ?? "")
        ? (current.default_coordinator_id ?? "")
        : "",
      assignments: Object.fromEntries(
        Object.entries(current.assignments ?? {}).map(([workflow, roles]) => [
          workflow,
          Object.fromEntries(
            Object.entries(roles ?? {}).filter(([, id]) => !id || ids.has(id)),
          ),
        ]),
      ),
    };
  };
  const memberOptions = (current: string) => [
    <option key="" value="">
      使用会话可用成员
    </option>,
    ...enabled.map((agent) => (
      <option key={agent.id} value={agent.id}>
        {agent.display_name} (@{agent.handle})
      </option>
    )),
    ...(current && !enabled.some((agent) => agent.id === current)
      ? [
          <option key="unavailable" value={current}>
            原成员已停用或不存在，保存时清除
          </option>,
        ]
      : []),
  ];
  return (
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
        <h3>默认分工</h3>
        <p>
          默认协调者和模式分工在创建会话与解析分工时使用。显式选择优先；成员未加入当前空间、已停用或缺少该角色所需工具时，回退到本会话可用成员，不会自动加入空间或授予权限。
        </p>
        <label>
          默认协调者
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
        <h3>模式角色</h3>
        <div className={cx("settings-fieldsets")}>
          {Object.entries(workflowRoles)
            .filter(([workflow]) => workflow !== "code_analysis")
            .map(([workflow, roles]) => (
              <fieldset key={workflow}>
                <legend>{workflows[workflow as keyof typeof workflows]}</legend>
                {roles.map((name) => (
                  <label key={name}>
                    {roleLabel(name)}
                    <Select
                      value={role(workflow, name)}
                      onChange={(selected) => {
                        setSaved(false);
                        choose(workflow, name, selected);
                      }}
                    >
                      {memberOptions(role(workflow, name))}
                    </Select>
                  </label>
                ))}
              </fieldset>
            ))}
        </div>
        {error && (
          <p role="alert" className={cx("error")}>
            {error}
          </p>
        )}
        {saved && <p role="status">协作默认已保存，新会话会使用这些选择。</p>}
        <div className={cx("form-actions")}>
          <button className={cx("primary")} disabled={saving}>
            {saving ? "保存中…" : "保存协作默认"}
          </button>
        </div>
      </section>
    </form>
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
        <h3>服务状态</h3>
        <p>
          成员调用本机 Claude、Codex 或 Gemini
          CLI，复用本机的模型、供应商与认证配置。网页不会读取凭证。
        </p>
        <div className={cx("settings-grid")}>
          <div className={cx("settings-card")}>
            <h4>执行限制</h4>
            <ul>
              {Object.entries(system.limits ?? {})
                .map(
                  ([name, value]) => [name, limitLabel(name, value)] as const,
                )
                .filter(([, label]) => label)
                .map(([name, label]) => (
                  <li key={name}>{label}</li>
                ))}
              <li>同一时间处理一轮协作，其余按顺序排队；可停止当前协作。</li>
            </ul>
          </div>
          <div className={cx("settings-card")}>
            <h4>本机 CLI</h4>
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
                          ? "可执行"
                          : item.installed
                            ? "不可执行"
                            : "未安装"}
                      </span>
                    </div>
                    <p>
                      {item.installed
                        ? `已安装 · ${item.version || "版本未知"}`
                        : "未检测到可执行文件"}
                      {item.adapter ? ` · 调用方式 ${item.adapter}` : ""}
                    </p>
                    <p className={cx("muted")}>
                      {item.installed
                        ? `工具隔离 ${
                            item.supported === false
                              ? "不可确认"
                              : item.isolation === "enforced"
                                ? "探测通过"
                                : item.isolation === "candidate"
                                  ? "待验证（首次执行前自动探测）"
                                  : "待确认"
                          }`
                        : "安装后才可探测工具隔离"}
                      {" · "}
                      {item.protocol === "verified"
                        ? "连接与模型已验证"
                        : item.installed
                          ? "连接未验证（运行时再检查协议）"
                          : "尚未连接"}
                    </p>
                    {item.reason && (
                      <p className={cx("muted")}>
                        {item.supported === false ? "不支持" : "说明"}：
                        {item.reason}
                      </p>
                    )}
                  </div>
                );
              })}
              {!system.executors?.length && (
                <p className={cx("muted")}>未检测到本机 CLI。</p>
              )}
            </div>
          </div>
          <div className={cx("settings-card")}>
            <h4>在线搜索</h4>
            <p>
              {statusText[system.search_status] ??
                system.search_status ??
                "未配置"}
              {system.search_provider ? ` · ${system.search_provider}` : ""}
            </p>
            <p className={cx("muted")}>
              {system.search_status === "configured"
                ? "搜索查询会通过网络发送给搜索服务。"
                : "搜索不可用时仍可读取公开 URL 和本地资料。"}
            </p>
          </div>
          <div className={cx("settings-card")}>
            <h4>资料目录范围</h4>
            <pre className={cx("pre")}>
              {system.roots.join("\n") || "在各工作空间设置中指定"}
            </pre>
          </div>
        </div>
        <p className={cx("muted")}>
          真实运行会通过 CLI
          发送问题与读取片段至模型服务；搜索会发送查询词。已安装不代表连接或模型可用。
        </p>
      </section>
      <section className={cx("settings-section")}>
        <h3>验证连接</h3>
        <p>
          只验证所选 CLI 与确切模型；留空时使用该 CLI
          的默认模型。验证可能产生少量用量。
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
              验证 CLI
              <Select
                value={executor}
                onChange={(selected) => {
                  setExecutor(selected);
                  setError("");
                }}
              >
                {(system.executors?.length
                  ? system.executors.map((item) => item.id)
                  : ["claude", "codex", "gemini", "pi"]
                ).map((id) => (
                  <option key={id} value={id}>
                    {id}
                  </option>
                ))}
              </Select>
            </label>
            <label>
              验证模型（留空使用 CLI 默认）
              <input
                value={model}
                onChange={(event) => {
                  setModel(event.target.value);
                  setError("");
                }}
                placeholder="CLI 默认模型"
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
              验证时间：{formatDateTime(system.verified[key])} · 该 CLI
              与模型上次已验证
            </p>
          )}
          <div className={cx("form-actions")}>
            <button className={cx("primary")} disabled={verifying || !runnable}>
              {verifying ? "正在验证…" : "验证模型连接（可能产生用量）"}
            </button>
          </div>
        </form>
      </section>
      <section className={cx("settings-section")}>
        <h3>锁定</h3>
        <div className={cx("settings-security")}>
          <p className={cx("muted")}>
            离开时可锁定页面，正在进行的协作会继续运行。
          </p>
          <button
            type="button"
            onClick={() => {
              void lock().catch((error) => setError(errorText(error)));
            }}
          >
            锁定工作空间
          </button>
        </div>
      </section>
    </>
  );
}

function TrashSettings({
  settings,
  saveSettings,
  onRestoreThread,
  onTrashChanged,
}: {
  settings?: Settings;
  saveSettings?: (value: SettingsInput) => Promise<Settings>;
  onRestoreThread?: (thread: Thread) => Promise<void> | void;
  onTrashChanged?: () => Promise<void> | void;
}) {
  const [items, setItems] = useState<TrashItem[]>();
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [confirm, setConfirm] = useState<{
    kind: "restore" | "permanent";
    item: TrashItem;
  }>();
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [days, setDays] = useState(String(settings?.retention_days ?? 7));
  const [savingDays, setSavingDays] = useState(false);
  const [savedDays, setSavedDays] = useState(false);
  const load = useCallback(async () => {
    setLoading(true);
    setLoadError("");
    try {
      const result = await api<{ items: TrashItem[] | null }>("/api/v1/trash");
      setItems(result.items ?? []);
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

  const restore = async (item: TrashItem) => {
    setBusy(item.thread.id);
    setError("");
    try {
      await api(
        `/api/v1/threads/${item.thread.id}/restore`,
        "POST",
        {},
        { "If-Match": String(item.thread.revision) },
      );
      setItems((current) =>
        (current ?? []).filter(
          (candidate) => candidate.thread.id !== item.thread.id,
        ),
      );
      setConfirm(undefined);
      await onRestoreThread?.(item.thread);
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setBusy("");
    }
  };
  const remove = async (item: TrashItem) => {
    setBusy(item.thread.id);
    setError("");
    try {
      await api(
        `/api/v1/threads/${item.thread.id}/permanent`,
        "DELETE",
        undefined,
        { "If-Match": String(item.thread.revision) },
      );
      setItems((current) =>
        (current ?? []).filter(
          (candidate) => candidate.thread.id !== item.thread.id,
        ),
      );
      setConfirm(undefined);
      await onTrashChanged?.();
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setBusy("");
    }
  };

  return (
    <>
      <section className={cx("settings-section")}>
        <h3>回收站</h3>
        <p>
          删除的会话会在这里保留 {settings?.retention_days ?? 7}{" "}
          天。恢复后回到原工作空间；永久删除会立即移除消息、协作记录、来源和产物，无法恢复。
        </p>
        {loading && (
          <p role="status" className={cx("muted")}>
            正在读取回收站…
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
              重试
            </button>
          </p>
        )}
        {items && !loading && !loadError && !items.length && (
          <p className={cx("muted")}>回收站为空。</p>
        )}
        {!!items?.length && (
          <ul className={cx("trash-list")} aria-label="回收站会话">
            {items.map((item) => {
              const active = confirm?.item.thread.id === item.thread.id;
              return (
                <li key={item.thread.id} className={cx("trash-item")}>
                  <div className={cx("trash-main")}>
                    <strong>{item.thread.title}</strong>
                    <span className={cx("trash-meta")}>
                      <span>{item.workspace_name}</span>
                      <span>
                        删除于{" "}
                        {formatDateTime(item.thread.deleted_at) || "未知时间"}
                      </span>
                      <span>
                        计划于{" "}
                        {formatDateTime(item.thread.purge_at) || "未知时间"}{" "}
                        自动清理
                      </span>
                    </span>
                  </div>
                  <div className={cx("trash-actions")}>
                    <button
                      type="button"
                      className={cx("ghost small")}
                      aria-label={`恢复会话：${item.thread.title}`}
                      disabled={!!busy}
                      onClick={() => {
                        setError("");
                        setConfirm({ kind: "restore", item });
                      }}
                    >
                      恢复
                    </button>
                    <button
                      type="button"
                      className={cx("danger small")}
                      aria-label={`永久删除会话：${item.thread.title}`}
                      disabled={!!busy}
                      onClick={() => {
                        setError("");
                        setConfirm({ kind: "permanent", item });
                      }}
                    >
                      永久删除
                    </button>
                  </div>
                  {active && (
                    <div
                      className={cx("confirm-inline")}
                      role="alertdialog"
                      aria-label={
                        confirm.kind === "restore"
                          ? `确认恢复 ${item.thread.title}`
                          : `确认永久删除 ${item.thread.title}`
                      }
                    >
                      <p>
                        {confirm.kind === "restore"
                          ? `恢复“${item.thread.title}”？它会回到“${item.workspace_name}”并打开。`
                          : `永久删除“${item.thread.title}”？消息、协作记录、来源和产物会立即移除，无法恢复。`}
                      </p>
                      <div className={cx("form-actions")}>
                        <button
                          type="button"
                          disabled={busy === item.thread.id}
                          onClick={() => setConfirm(undefined)}
                        >
                          取消
                        </button>
                        <button
                          type="button"
                          className={cx(
                            confirm.kind === "permanent" ? "danger" : "primary",
                          )}
                          disabled={busy === item.thread.id}
                          onClick={() =>
                            void (confirm.kind === "restore"
                              ? restore(item)
                              : remove(item))
                          }
                        >
                          {busy === item.thread.id
                            ? "正在处理…"
                            : confirm.kind === "restore"
                              ? "确认恢复"
                              : "确认永久删除"}
                        </button>
                      </div>
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
        )}
        {error && (
          <p role="alert" className={cx("error")}>
            {error}
          </p>
        )}
      </section>
      {settings && saveSettings && (
        <section className={cx("settings-section")}>
          <h3>保留周期</h3>
          <p className={cx("muted")}>
            保存后会同步重算回收站中会话的清理时间；之后删除的会话也使用新周期。
          </p>
          <form
            className={cx("settings-form settings-inline-form")}
            onSubmit={(event) => {
              event.preventDefault();
              const next = Number(days);
              if (!Number.isInteger(next) || next < 1 || next > 3650) {
                setError("保留天数必须是 1 到 3650 之间的整数。");
                return;
              }
              setSavingDays(true);
              setSavedDays(false);
              setError("");
              void saveSettings({
                default_coordinator_id: settings.default_coordinator_id ?? "",
                assignments: Object.fromEntries(
                  Object.entries(settings.assignments ?? {}).map(
                    ([workflow, roles]) => [workflow, { ...(roles ?? {}) }],
                  ),
                ),
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
              回收站保留天数
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
              {savingDays ? "保存中…" : "保存保留周期"}
            </button>
            {savedDays && (
              <p role="status">
                保留周期已保存，回收站中的清理时间已同步更新。
              </p>
            )}
          </form>
        </section>
      )}
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
  const workflow =
    workflows[(run.input.workflow || "free") as keyof typeof workflows];
  // Only the persisted method/selection reaches the snapshot; a missing record
  // stays the free label instead of being inferred from messages.
  const method = runMethod(run);
  const policyRows = executionPolicyRows(run.execution_policy);
  const startedAt = formatDateTime(run.started_at);
  const deadlineAt = formatDateTime(run.deadline_at);
  return (
    <Modal title="本次协作配置快照" close={close}>
      <p>
        {workflow} · 创建于 {formatDateTime(run.created_at)}
      </p>
      <p>本轮方法：{method.label}</p>
      {method.reason && (
        <p className={cx("muted")}>选择原因：{method.reason}</p>
      )}
      {run.coordinator_id && (
        <p>
          本轮协调者：
          {run.agents?.find((agent) => agent.id === run.coordinator_id)
            ?.display_name || "成员"}
        </p>
      )}
      <section className={cx("snapshot-member")}>
        <h3>执行策略（本次协作冻结）</h3>
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
          <p className={cx("muted")}>{legacyPolicyNote}</p>
        )}
        {startedAt && <p>开始时间：{startedAt}</p>}
        {run.execution_policy &&
          (run.execution_policy.run_timeout_ns > 0 ? (
            <p>
              {deadlineAt
                ? `截止时间：${deadlineAt}`
                : startedAt
                  ? "截止时间：未记录"
                  : "尚未派发，期限从首次执行开始计算"}
            </p>
          ) : (
            <p>截止时间：不设绝对期限</p>
          ))}
        {!run.execution_policy && deadlineAt && <p>截止时间：{deadlineAt}</p>}
        {policyRows.length > 0 && <p className={cx("muted")}>{policyNote}</p>}
      </section>
      {run.agents?.map((agent) => (
        <section className={cx("snapshot-member")} key={agent.id}>
          <h3>
            {agent.display_name} (@{agent.handle})
          </h3>
          <p className={cx("muted")}>
            配置版本 {agent.revision} ·{" "}
            {agent.executor ? `${agent.executor} · ` : ""}
            {agent.model === "default" ? "CLI 默认模型" : agent.model}
          </p>
          {agent.role_description && <p>{agent.role_description}</p>}
          <p className={cx("muted")}>可用工具：{agent.tools?.length ?? 0} 项</p>
        </section>
      ))}
      {run.skill_versions && (
        <section className={cx("snapshot-member")}>
          <h3>共享协作技能</h3>
          {Object.entries(run.skill_versions).map(([id, version]) => (
            <p key={id}>
              {skillLabel(id)} ·{" "}
              <code title={version}>{version.slice(0, 12)}</code>
            </p>
          ))}
          {!Object.keys(run.skill_versions).length && (
            <p className={cx("muted")}>本次协作未启用共享技能。</p>
          )}
        </section>
      )}
      <p className={cx("muted")}>
        历史快照不随成员改名、模型或权限编辑而改变。
      </p>
    </Modal>
  );
}
