import { Select } from "./Select";
import { useState } from "react";
import type {
  Agent,
  Run,
  Settings,
  SettingsInput,
  System,
} from "../api/client";
import { api, statusText } from "../api/client";
import { cx } from "../styles/classes";
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
import { Modal } from "./Modal";

type SettingsTab = "members" | "collaboration" | "service";

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
}: {
  system: System;
  setSystem: (value: System) => void;
  close: () => void;
  lock: () => Promise<void>;
  settings?: Settings;
  saveSettings?: (value: SettingsInput) => Promise<Settings>;
  agents?: Agent[];
  saveAgent?: (value: Agent) => Promise<Agent>;
}) {
  const tabs: [SettingsTab, string][] = [];
  if (saveAgent) tabs.push(["members", "成员"]);
  if (settings && saveSettings) tabs.push(["collaboration", "协作"]);
  tabs.push(["service", "服务"]);
  const [tab, setTab] = useState<SettingsTab>(tabs[0][0]);
  return (
    <Modal title="设置" close={close} wide>
      <div className={cx("panel-tabs")} role="tablist" aria-label="设置分类">
        {tabs.map(([key, label]) => (
          <button
            key={key}
            type="button"
            role="tab"
            id={`settings-tab-${key}`}
            aria-selected={tab === key}
            aria-controls={`settings-panel-${key}`}
            onClick={() => setTab(key)}
          >
            {label}
          </button>
        ))}
      </div>
      <div
        id={`settings-panel-${tab}`}
        role="tabpanel"
        aria-labelledby={`settings-tab-${tab}`}
      >
        {tab === "members" && saveAgent && (
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
        {tab === "collaboration" && settings && saveSettings && (
          <CollaborationSettings
            agents={agents}
            settings={settings}
            save={saveSettings}
          />
        )}
        {tab === "service" && (
          <ServiceSettings
            system={system}
            setSystem={setSystem}
            lock={lock}
          />
        )}
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
            Object.entries(roles ?? {}).filter(
              ([, id]) => !id || ids.has(id),
            ),
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
      {Object.entries(workflowRoles).map(([workflow, roles]) => (
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
      <p>
        成员调用本机 Claude、Codex 或 Gemini
        CLI，复用本机的模型、供应商与认证配置。网页不会读取凭证。
      </p>
      <dl>
        <dt>执行限制</dt>
        <dd>
          <ul>
            {Object.entries(system.limits ?? {})
              .map(([name, value]) => [name, limitLabel(name, value)] as const)
              .filter(([, label]) => label)
              .map(([name, label]) => (
                <li key={name}>{label}</li>
              ))}
            <li>同一时间处理一轮协作，其余按顺序排队；可停止当前协作。</li>
          </ul>
        </dd>
        <dt>本机 CLI</dt>
        <dd>
          {system.executors?.map((item) => (
            <p key={item.id}>
              {item.id} ·{" "}
              {item.installed ? `已安装 · ${item.version}` : "未检测到"}
              {item.adapter ? ` · 调用方式 ${item.adapter}` : ""}
              {item.installed
                ? ` · 工具隔离 ${
                    item.supported === false
                      ? "不可确认"
                      : item.isolation === "enforced"
                        ? "探测通过"
                        : item.isolation === "candidate"
                          ? "待验证（首次执行前自动探测）"
                          : "待确认"
                  }`
                : ""}
              {item.protocol === "verified"
                ? " · 协议与模型连接已验证"
                : item.installed
                  ? " · 连接状态：未验证（运行时再检查协议）"
                  : ""}
              {item.reason
                ? ` · ${item.supported === false ? "不支持" : "说明"}：${item.reason}`
                : ""}
            </p>
          ))}
        </dd>
        <dt>在线搜索</dt>
        <dd>
          {statusText[system.search_status]}
          {system.search_provider ? ` · ${system.search_provider}` : ""}
        </dd>
        <dt>资料目录范围</dt>
        <dd>{system.roots.join("\n") || "在各工作空间设置中指定"}</dd>
      </dl>
      <p className={cx("muted")}>
        真实运行会通过 CLI
        发送问题与读取片段至模型服务；搜索会发送查询词。已安装不代表连接或模型可用。
      </p>
      <form
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
        <label>
          验证 CLI
          <Select
            value={executor}
            onChange={(selected) => {
              setExecutor(selected);
              setError("");
            }}
          >
            {["claude", "codex", "gemini"].map((id) => (
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
        {!run.execution_policy && deadlineAt && (
          <p>截止时间：{deadlineAt}</p>
        )}
        {policyRows.length > 0 && (
          <p className={cx("muted")}>{policyNote}</p>
        )}
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
