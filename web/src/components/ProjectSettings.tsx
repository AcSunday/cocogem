import { useCallback, useEffect, useState } from "react";
import {
  api,
  type Agent,
  type BudgetPolicy,
  type BudgetPolicyOverride,
  type Grant,
  type Settings,
  type Workspace,
} from "../api/client";
import { errorText } from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import styles from "../styles/project.module.css";

const cx = scoped(base, controls, styles);
const fields: [keyof BudgetPolicy, keyof BudgetPolicyOverride, string][] = [
  ["auto_continuations", "auto_continuations", "自动续办次数"],
  ["parallel_branches", "parallel_branches", "并行分支数"],
  ["writer_branches", "writer_branches", "写分支数"],
  ["fix_rounds", "fix_rounds", "修复轮次"],
  ["no_progress_failures", "no_progress_failures", "连续无进展失败"],
];

const modes = [
  {
    value: "controlled",
    label: "受控",
    description: "使用宿主工具、隔离目录和显式授权；首次与迁移默认。",
  },
  {
    value: "project_autonomous",
    label: "项目内自主",
    description: "项目范围内连续执行；隔离能力未验证时仍回退受控。",
  },
  {
    value: "full_access",
    label: "完全访问",
    description: "明确信任本机能力、命令与既有凭据；不是沙箱隔离。",
  },
];

const actionLabels: Record<string, string> = {
  read: "读取",
  write: "写入",
  run_test: "运行测试",
  network: "联网",
  publish_artifact: "发布产物",
  collaborate: "协作",
  native_read: "原生读取",
  native_write: "原生写入",
  native_execute: "原生执行",
};

const grantStatus: Record<string, string> = {
  active: "有效",
  revoking: "撤销中",
  revoked: "已撤销",
  revocation_uncertain: "撤销待核实",
};

/** Project-level execution, grants and future root-task budget defaults. */
export function ProjectSettings({
  workspace,
  settings,
  agents = [],
  onWorkspaceChange,
}: {
  workspace: Workspace;
  settings?: Settings;
  agents?: Agent[];
  onWorkspaceChange: (workspace: Workspace) => void;
}) {
  const [mode, setMode] = useState(workspace.execution_mode || "controlled");
  const [override, setOverride] = useState<Record<string, string>>({});
  const [grants, setGrants] = useState<Grant[]>([]);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    try {
      const page = await api<{ items: Grant[] }>(
        `/api/v1/workspaces/${workspace.id}/grants`,
      );
      setGrants(page.items ?? []);
      setError("");
    } catch (failure) {
      setError(errorText(failure));
    }
  }, [workspace.id]);
  useEffect(() => {
    setMode(workspace.execution_mode || "controlled");
    const next: Record<string, string> = {};
    for (const [, overrideKey, label] of fields) {
      const value = workspace.budget_override?.[overrideKey];
      if (typeof value === "number") next[label] = String(value);
    }
    setOverride(next);
    void load();
  }, [workspace, load]);

  const save = async (name: string, action: () => Promise<Workspace>) => {
    if (busy) return;
    setBusy(name);
    setError("");
    try {
      onWorkspaceChange(await action());
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setBusy("");
    }
  };
  const defaults = settings?.collaboration_budget;
  const memberName = (id: string) =>
    agents.find((agent) => agent.id === id)?.display_name ||
    agents.find((agent) => agent.id === id)?.handle ||
    id;
  return (
    <div className={cx("project-settings-panel")}>
      <header className={cx("settings-panel-head")}>
        <div>
          <h3>权限与预算</h3>
          <p>项目级设置会立即影响后续执行；历史 Run 保留冻结授权快照。</p>
        </div>
      </header>
      {error && (
        <p className={cx("error")} role="alert">
          {error}
        </p>
      )}

      <section className={cx("settings-card")} aria-labelledby="project-execution-mode">
        <div className={cx("settings-card-head")}>
          <h4 id="project-execution-mode">执行权限模式</h4>
          <p>切换会停止受影响项目的在途执行，不重置协作预算。</p>
        </div>
        <div className={cx("mode-options")} role="radiogroup" aria-label="执行权限模式">
          {modes.map((option) => (
            <label
              key={option.value}
              className={cx("mode-option")}
              data-selected={mode === option.value || undefined}
            >
              <input
                type="radio"
                name="execution-mode"
                value={option.value}
                checked={mode === option.value}
                onChange={() => setMode(option.value)}
              />
              <span>
                <strong>{option.label}</strong>
                <small>{option.description}</small>
              </span>
            </label>
          ))}
        </div>
        <div className={cx("settings-card-actions")}>
          <button
            className={cx("primary")}
            disabled={!!busy || mode === (workspace.execution_mode || "controlled")}
            onClick={() =>
              void save("mode", () =>
                api(
                  `/api/v1/workspaces/${workspace.id}/execution-policy`,
                  "PATCH",
                  { mode, source_message_id: crypto.randomUUID() },
                  { "If-Match": String(workspace.revision) },
                ),
              )
            }
          >
            保存权限模式
          </button>
        </div>
      </section>

      <section className={cx("settings-card")} aria-labelledby="project-budget-defaults">
        <div className={cx("settings-card-head")}>
          <h4 id="project-budget-defaults">新根任务默认预算</h4>
          <p>留空继承全局设置；只影响以后创建的根任务，现有任务保持原预算。</p>
        </div>
        <form
          onSubmit={(event) => {
            event.preventDefault();
            const value: BudgetPolicyOverride = {};
            for (const [, key, label] of fields) {
              if (override[label]?.trim()) value[key] = Number(override[label]);
            }
            void save("budget", () =>
              api(
                `/api/v1/workspaces/${workspace.id}/budget-policy`,
                "PATCH",
                { override: value },
                { "If-Match": String(workspace.revision) },
              ),
            );
          }}
        >
          <div className={cx("budget-fields")}>
            {fields.map(([defaultKey, , label]) => (
              <label className={cx("budget-field")} key={label}>
                <span>{label}</span>
                <input
                  type="number"
                  aria-label={label}
                  min={1}
                  value={override[label] ?? ""}
                  placeholder={defaults ? String(defaults[defaultKey]) : "继承"}
                  onChange={(event) =>
                    setOverride((current) => ({
                      ...current,
                      [label]: event.target.value,
                    }))
                  }
                />
                <small>继承 {defaults ? defaults[defaultKey] : "全局"}</small>
              </label>
            ))}
          </div>
          <div className={cx("settings-card-actions")}>
            <button className={cx("primary")} disabled={!!busy}>
              保存项目覆盖
            </button>
            <button
              type="button"
              disabled={!!busy}
              onClick={() => {
                setOverride({});
                void save("budget-reset", () =>
                  api(
                    `/api/v1/workspaces/${workspace.id}/budget-policy`,
                    "PATCH",
                    { override: null },
                    { "If-Match": String(workspace.revision) },
                  ),
                );
              }}
            >
              全部恢复继承
            </button>
          </div>
        </form>
      </section>

      <section className={cx("settings-card")} aria-labelledby="project-grants">
        <div className={cx("settings-card-head")}>
          <h4 id="project-grants">持续授权</h4>
          <p>成员能力上限与实际项目授权取交集；撤销后新执行立即被拒绝。</p>
        </div>
        {grants.length ? (
          <div className={cx("grant-list")}>
            {grants.map((grant) => (
              <article className={cx("grant-row")} key={grant.id}>
                <div className={cx("grant-main")}>
                  <div className={cx("grant-title")}>
                    <strong>{memberName(grant.subject_member_id)}</strong>
                    <span className={cx(`grant-status ${grant.status}`)}>
                      {grantStatus[grant.status] || grant.status}
                    </span>
                  </div>
                  <small>
                    {grant.actions?.map((action) => actionLabels[action] || action).join("、") || "无动作"} ·{" "}
                    {grant.resources?.some((item) => item.startsWith("workspace:"))
                      ? "项目范围"
                      : grant.resources?.join("、") || "无资源"}{" "}
                    · 代次 {grant.epoch}
                    {grant.expires_at ? ` · 到期 ${grant.expires_at}` : ""}
                  </small>
                </div>
                {["active", "revocation_uncertain"].includes(grant.status) && (
                  <button
                    type="button"
                    className={cx("small")}
                    disabled={!!busy}
                    onClick={() => {
                      setBusy(`revoke-${grant.id}`);
                      void api(
                        `/api/v1/workspaces/${workspace.id}/grants/${grant.subject_member_id}/revoke`,
                        "POST",
                        { source_message_id: crypto.randomUUID() },
                      )
                        .then(load)
                        .catch((failure) => setError(errorText(failure)))
                        .finally(() => setBusy(""));
                    }}
                  >
                    撤销
                  </button>
                )}
              </article>
            ))}
          </div>
        ) : (
          <p className={cx("project-empty")}>
            尚无持续授权记录；首次受控执行不会自动扩大权限。
          </p>
        )}
      </section>
    </div>
  );
}
