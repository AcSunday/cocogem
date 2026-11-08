import { useEffect, useState } from "react";
import { api, type Agent, type Settings, type Workspace } from "../api/client";
import { errorText } from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import styles from "../styles/project.module.css";
import { executionModeOf, executionModes } from "../app/useGlobalSettings";

const cx = scoped(base, controls, styles);

/**
 * Workspace-level context only. The execution mode, conversation grants and the
 * future root-task budget are global settings (设置 → 权限 / 协作), so this panel
 * never offers a workspace override: a second effective configuration would be
 * a hidden permission source. The workspace keeps its name, description and
 * directory binding in `WorkspaceForm`; this panel explains the global state
 * and links nowhere that could silently change it.
 */
export function ProjectSettings({
  workspace,
  settings,
  agents = [],
  onWorkspaceChange,
}: {
  workspace: Workspace;
  settings?: Settings;
  agents?: Agent[];
  onWorkspaceChange?: (workspace: Workspace) => void;
}) {
  // The workspace mode/budget fields are inert audit data; keep the prop for
  // callers that still render the panel inside a workspace form.
  void onWorkspaceChange;
  const mode = executionModeOf(settings);
  const option = executionModes.find((item) => item.value === mode);
  return (
    <div className={cx("project-settings-panel")}>
      <header className={cx("settings-panel-head")}>
        <div>
          <h3>继承的全局设置</h3>
          <p>
            权限与根任务预算属于整个 Cocogem，不在这里按工作空间覆盖。切换空间不会改变权限，历史 Run 继续使用冻结快照。
          </p>
        </div>
      </header>

      <section className={cx("settings-card")} aria-label="当前全局权限">
        <div className={cx("settings-card-head")}>
          <h4>执行权限</h4>
          <p>由设置 → 权限统一管理；输入框下方的权限选择与这里读取同一个值。</p>
        </div>
        <p className={cx("mode-readout")} data-mode={mode}>
          <strong>{option?.label ?? "项目内自主"}</strong>
          <small>{option?.summary ?? "项目内自主，项目外申请"}</small>
        </p>
      </section>

      <section className={cx("settings-card")} aria-label="本空间成员">
        <div className={cx("settings-card-head")}>
          <h4>成员与执行</h4>
          <p>
            成员是否加入并在本空间启用仍按空间管理；成员的工具与命令边界不再按空间或成员授权，而由全局权限决定。
          </p>
        </div>
        <p className={cx("project-empty")}>
          {agents.length
            ? `${agents.length} 位成员可用于本空间。`
            : "本空间还没有成员。可在成员管理中把全局档案加入本空间。"}
        </p>
      </section>

      <section className={cx("settings-card")} aria-label="本空间根预算">
        <div className={cx("settings-card-head")}>
          <h4>新根任务预算</h4>
          <p>
            新建根任务读取设置 → 协作中的全局预算默认值。已有根任务的账本、预留和子任务关联保持不变，不再有空间覆盖。
          </p>
        </div>
        <p className={cx("project-empty")}>
          {settings?.collaboration_budget
            ? `续办 ${settings.collaboration_budget.auto_continuations} · 并行 ${settings.collaboration_budget.parallel_branches} · 写分支 ${settings.collaboration_budget.writer_branches} · 修复 ${settings.collaboration_budget.fix_rounds} · 无进展 ${settings.collaboration_budget.no_progress_failures}`
            : "尚未读取全局预算默认值。"}
        </p>
      </section>

      <section className={cx("settings-card")} aria-label="本空间持续授权">
        <div className={cx("settings-card-head")}>
          <h4>持续授权</h4>
          <p>
            持续授权按会话管理：设置 → 权限可以查看并撤销当前会话的授权记录。本空间不再保存独立授权。
          </p>
        </div>
        <p className={cx("project-empty")}>
          当前空间：{workspace.name || "未命名"}。
        </p>
      </section>
    </div>
  );
}

/**
 * Reads the current global settings for a panel that has no parent state, so a
 * standalone workspace dialog still shows the same truth as the settings page.
 */
export function useReadonlySettings(initial?: Settings) {
  const [settings, setSettings] = useState<Settings | undefined>(initial);
  const [error, setError] = useState("");
  useEffect(() => {
    let canceled = false;
    void api<Settings>("/api/v1/settings")
      .then((value) => {
        if (!canceled) setSettings(value);
      })
      .catch((failure) => {
        if (!canceled) setError(errorText(failure));
      });
    return () => {
      canceled = true;
    };
  }, []);
  return { settings, error };
}
