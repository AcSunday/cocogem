import { useEffect, useState } from "react";
import { api, type Agent, type Settings, type Workspace } from "../api/client";
import { t, useLanguage } from "../lib/i18n";
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
  useLanguage();
  // The workspace mode/budget fields are inert audit data; keep the prop for
  // callers that still render the panel inside a workspace form.
  void onWorkspaceChange;
  const mode = executionModeOf(settings);
  const option = executionModes.find((item) => item.value === mode);
  return (
    <div className={cx("project-settings-panel")}>
      <header className={cx("settings-panel-head")}>
        <div>
          <h3>{t("继承的全局设置", "Inherited global settings")}</h3>
          <p>
            {t(
              "权限与主任务预算属于整个 Cocogem 全局共享，不按工作空间单独覆盖。切换空间不会改变全局权限，历史协作记录继续保留当时的配置快照。",
              "Permissions and main-task budgets are shared across Cocogem rather than overridden per project. Switching projects does not change permissions, and historical runs retain their frozen snapshot.",
            )}
          </p>
        </div>
      </header>

      <section
        className={cx("settings-card")}
        aria-label={t("当前全局权限", "Current global permissions")}
      >
        <div className={cx("settings-card-head")}>
          <h4>{t("执行权限", "Execution permissions")}</h4>
          <p>
            {t(
              "由「设置 → 权限」统一管理；输入框下方的权限选择与这里读取同一个值。",
              "Managed under Settings → Permissions; the selector below the composer reads and writes the same value.",
            )}
          </p>
        </div>
        <p className={cx("mode-readout")} data-mode={mode}>
          <strong>{option?.label ?? t("沙箱自主", "Sandboxed")}</strong>
          <small>
            {option?.summary ??
              t(
                "工作台内零弹窗，越界才申请",
                "Auto-run inside workspace; prompt only outside",
              )}
          </small>
        </p>
      </section>

      <section
        className={cx("settings-card")}
        aria-label={t("本空间成员", "Project members")}
      >
        <div className={cx("settings-card-head")}>
          <h4>{t("成员与执行", "Members & execution")}</h4>
          <p>
            {t(
              "成员是否加入并在本空间启用仍按空间管理；成员的工具与命令边界由全局权限统一决定。",
              "Which members join and are enabled is managed per project; their tool and command boundaries follow the global permission mode.",
            )}
          </p>
        </div>
        <p className={cx("project-empty")}>
          {agents.length
            ? t(
                `${agents.length} 位成员可用于本空间。`,
                `${agents.length} member(s) available in this project.`,
              )
            : t(
                "本空间还没有成员。可在成员管理中把全局档案加入本空间。",
                "No members in this project yet. Add global member profiles from the Members dialog.",
              )}
        </p>
      </section>

      <section
        className={cx("settings-card")}
        aria-label={t("本空间根预算", "Default task budget")}
      >
        <div className={cx("settings-card-head")}>
          <h4>{t("新建主任务预算", "New main-task budget")}</h4>
          <p>
            {t(
              "新建主任务使用「设置 → 协作」中的全局预算默认值；已有任务的额度与子任务关联保持不变。",
              "Newly created main tasks use the global budget defaults from Settings → Collaboration; existing tasks keep their current allowance.",
            )}
          </p>
        </div>
        <p className={cx("project-empty")}>
          {settings?.collaboration_budget
            ? t(
                `续办 ${settings.collaboration_budget.auto_continuations} · 并行 ${settings.collaboration_budget.parallel_branches} · 写分支 ${settings.collaboration_budget.writer_branches} · 修复 ${settings.collaboration_budget.fix_rounds} · 无进展 ${settings.collaboration_budget.no_progress_failures}`,
                `Auto-continue ${settings.collaboration_budget.auto_continuations} · Parallel ${settings.collaboration_budget.parallel_branches} · Writers ${settings.collaboration_budget.writer_branches} · Fix rounds ${settings.collaboration_budget.fix_rounds} · No-progress ${settings.collaboration_budget.no_progress_failures}`,
              )
            : t(
                "尚未读取全局预算默认值。",
                "Global budget defaults not loaded yet.",
              )}
        </p>
      </section>

      <section
        className={cx("settings-card")}
        aria-label={t("本空间持续授权", "Session-wide approvals")}
      >
        <div className={cx("settings-card-head")}>
          <h4>{t("持续授权", "Session-wide approvals")}</h4>
          <p>
            {t(
              "持续授权按会话管理：可在「设置 → 权限」中查看并撤销当前会话的授权记录。",
              "Session-wide approvals are managed per conversation: view and revoke them under Settings → Permissions.",
            )}
          </p>
        </div>
        <p className={cx("project-empty")}>
          {t(
            `当前空间：${workspace.name || "未命名"}。`,
            `Current project: ${workspace.name || "Untitled"}.`,
          )}
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
