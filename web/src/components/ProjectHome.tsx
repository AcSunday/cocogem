import { useCallback, useEffect, useState } from "react";
import { api, type ProjectOverview, type Workspace } from "../api/client";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import conversation from "../styles/conversation.module.css";
import styles from "../styles/project.module.css";
import { Welcome } from "./Welcome";
import type { WorkflowKey } from "./Composer";

const cx = scoped(base, conversation, styles);

/** Main-branch welcome surface with a deliberately small durable summary. */
export function ProjectHome({
  workspace,
  members,
  onCreate,
  onPick,
  onOpenTasks,
  onEditWorkspace,
}: {
  workspace: Workspace;
  members: number;
  onCreate: () => void;
  onPick: (key: WorkflowKey, sample: string) => void;
  onOpenTasks: () => void;
  onEditWorkspace: () => void;
}) {
  const [summary, setSummary] = useState<ProjectOverview>();
  const load = useCallback(async () => {
    try {
      setSummary(
        await api<ProjectOverview>(
          `/api/v1/workspaces/${workspace.id}/overview`,
        ),
      );
    } catch {
      // The welcome surface stays usable when the optional summary is offline.
    }
  }, [workspace.id]);
  useEffect(() => {
    setSummary(undefined);
    void load();
  }, [load]);
  useEffect(() => {
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void load();
    }, 10000);
    return () => window.clearInterval(timer);
  }, [load]);

  const tasks = summary?.tasks ?? [];
  const open = tasks.filter(
    (item) => !["completed", "canceled"].includes(item.task.status),
  ).length;
  const completed = tasks.filter(
    (item) => item.task.status === "completed",
  ).length;
  const attention = summary?.needs_attention?.length ?? 0;
  const goal = summary?.records?.find((record) =>
    ["goal", "decision", "constraint", "rule"].includes(record.kind),
  )?.content;

  return (
    <div className={cx("project-home-page")}>
      <Welcome
        workspace={workspace}
        members={members}
        onCreate={onCreate}
        onPick={onPick}
      />
      {summary && (
        <section className={cx("project-summary")} aria-label="项目摘要">
          <div className={cx("project-summary-main")}>
            <span className={cx("eyebrow")}>项目摘要</span>
            <p>
              {goal ||
                workspace.description ||
                "目标、任务和验收记录会在这里保留。"}
            </p>
          </div>
          <div className={cx("project-summary-metrics")}>
            <span>
              <strong>{open}</strong> 待办
            </span>
            <span className={attention ? cx("attention") : undefined}>
              <strong>{attention}</strong> 需要你
            </span>
            <span>
              <strong>{completed}</strong> 已验收
            </span>
            <button type="button" className={cx("small")} onClick={onEditWorkspace}>
              编辑项目
            </button>
            <button type="button" className={cx("small")} onClick={onOpenTasks}>
              打开任务清单
            </button>
          </div>
        </section>
      )}
    </div>
  );
}
