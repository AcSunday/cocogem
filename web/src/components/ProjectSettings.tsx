import { useCallback, useEffect, useState } from "react";
import {
  api,
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

/** Project defaults and durable grants; existing task ledgers remain unchanged. */
export function ProjectSettings({
  workspace,
  settings,
  onWorkspaceChange,
}: {
  workspace: Workspace;
  settings?: Settings;
  onWorkspaceChange: (workspace: Workspace) => void;
}) {
  const [mode, setMode] = useState(workspace.execution_mode || "controlled");
  const [override, setOverride] = useState<Record<string, string>>({});
  const [grants, setGrants] = useState<Grant[]>([]);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    try {
      const page = await api<{ items: Grant[] }>(`/api/v1/workspaces/${workspace.id}/grants`);
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
  return (
    <details className={cx("project-settings")}>
      <summary>项目设置 · 权限、授权与默认预算</summary>
      {error && <p className={cx("error")} role="alert">{error}</p>}
      <section aria-labelledby="project-execution-mode">
        <h2 id="project-execution-mode">执行权限模式</h2>
        <p className={cx("section-note")}>受控使用宿主工具与隔离边界；项目内自主允许项目范围内的连续执行；完全访问是明确的本机信任决定，不代表沙箱。实际执行能力与回退以运行记录为准。</p>
        <div className={cx("inline-form")}>
          <label>模式<select value={mode} onChange={(event) => setMode(event.target.value)}>
            <option value="controlled">受控</option>
            <option value="project_autonomous">项目内自主</option>
            <option value="full_access">完全访问</option>
          </select></label>
          <button disabled={!!busy || mode === (workspace.execution_mode || "controlled")} onClick={() => void save("mode", () => api(`/api/v1/workspaces/${workspace.id}/execution-policy`, "PATCH", { mode, source_message_id: crypto.randomUUID() }, { "If-Match": String(workspace.revision) }))}>保存权限模式</button>
        </div>
      </section>
      <section aria-labelledby="project-budget-defaults">
        <h2 id="project-budget-defaults">新根任务的默认预算</h2>
        <p className={cx("section-note")}>留空继承全局设置；仅影响以后创建的根任务，现有任务保持原预算。</p>
        <form onSubmit={(event) => {
          event.preventDefault();
          const value: BudgetPolicyOverride = {};
          for (const [, key, label] of fields) {
            if (override[label]?.trim()) value[key] = Number(override[label]);
          }
          void save("budget", () => api(`/api/v1/workspaces/${workspace.id}/budget-policy`, "PATCH", { override: value }, { "If-Match": String(workspace.revision) }));
        }}>
          <div className={cx("budget-grid")}>{fields.map(([defaultKey, , label]) => <label key={label}>{label}<input type="number" min={1} value={override[label] ?? ""} placeholder={defaults ? String(defaults[defaultKey]) : "继承"} onChange={(event) => setOverride((current) => ({ ...current, [label]: event.target.value }))} /></label>)}</div>
          <div className={cx("task-actions")}><button className={cx("primary")} disabled={!!busy}>保存项目覆盖</button><button type="button" disabled={!!busy} onClick={() => {
            setOverride({});
            void save("budget-reset", () => api(`/api/v1/workspaces/${workspace.id}/budget-policy`, "PATCH", { override: null }, { "If-Match": String(workspace.revision) }));
          }}>全部恢复继承</button></div>
        </form>
      </section>
      <section aria-labelledby="project-grants">
        <h2 id="project-grants">持续授权</h2>
        {grants.length ? grants.map((grant) => <article className={cx("project-record")} key={grant.id}>
          <strong>{grant.subject_member_id} · {grant.status}</strong>
          <span>{grant.actions?.join("、") || "无动作"} · {grant.resources?.join("、") || "无资源"} · 代次 {grant.epoch}{grant.expires_at ? ` · 到期 ${grant.expires_at}` : ""}</span>
          {["active", "revocation_uncertain"].includes(grant.status) && <button className={cx("small")} disabled={!!busy} onClick={() => {
            setBusy(`revoke-${grant.id}`);
            void api(`/api/v1/workspaces/${workspace.id}/grants/${grant.subject_member_id}/revoke`, "POST", { source_message_id: crypto.randomUUID() }).then(load).catch((failure) => setError(errorText(failure))).finally(() => setBusy(""));
          }}>撤销此成员授权</button>}
        </article>) : <p className={cx("project-empty")}>尚无持续授权记录；首次受控执行不会自动扩大权限。</p>}
      </section>
    </details>
  );
}
