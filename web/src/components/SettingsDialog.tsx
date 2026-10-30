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
  if (saveAgent) tabs.push(["members", "成员管理"]);
  if (settings && saveSettings) tabs.push(["collaboration", "协作设置"]);
  tabs.push(["service", "服务与验证"]);
  const [tab, setTab] = useState<SettingsTab>(tabs[0][0]);
  
  return (
    <Modal title="设置" close={close} wide>
      <div className={cx("settings-container")}>
        <nav className={cx("settings-sidebar")}>
          {tabs.map(([key, label]) => (
            <button
              key={key}
              className={cx(`settings-tab ${tab === key ? "active" : ""}`)}
              onClick={() => setTab(key)}
            >
              {label}
            </button>
          ))}
        </nav>
        <main className={cx("settings-body")}>
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
        </main>
      </div>
    </Modal>
  );
}
// ... rest of the functions (CollaborationSettings, ServiceSettings, SnapshotDialog) remain largely unchanged in logic but should be updated with new styles if possible. For now, focus on structure.


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
  const [contextSize, setContextSize] = useState("");
  const [thinkingLevel, setThinkingLevel] = useState("0");
  const [verifying, setVerifying] = useState(false);
  const [error, setError] = useState("");
  const key = `${executor}:${model.trim() || "default"}`;
  
  return (
    <>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          setVerifying(true);
          setError("");
          void api("/api/v1/system/verify", "POST", {
            executor,
            model: model.trim() || "default",
            context_size: parseInt(contextSize) || undefined,
            thinking_level: parseInt(thinkingLevel) || 0,
          })
            .then(() => api<System>("/api/v1/system"))
            .then(setSystem)
            .catch((error) => setError(errorText(error)))
            .finally(() => setVerifying(false));
        }}
      >
        <label>
          验证 CLI
          <Select value={executor} onChange={setExecutor}>
            {["claude", "codex", "gemini"].map((id) => (
              <option key={id} value={id}>{id}</option>
            ))}
          </Select>
        </label>
        <label>
          验证模型（留空使用默认）
          <input value={model} onChange={(e) => setModel(e.target.value)} />
        </label>
        <label>
          上下文大小（留空使用默认）
          <input type="number" value={contextSize} onChange={(e) => setContextSize(e.target.value)} />
        </label>
        <label>
          思考等级（0-3，默认0）
          <input type="number" min="0" max="3" value={thinkingLevel} onChange={(e) => setThinkingLevel(e.target.value)} />
        </label>
        {error && <p className={cx("error")}>{error}</p>}
        <button type="submit" className={cx("primary")} disabled={verifying}>
          {verifying ? "验证中…" : "验证模型连接"}
        </button>
      </form>
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
