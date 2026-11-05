import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type { ProjectOverview as Overview, Settings, Workspace } from "../api/client";
import { ProjectSettings } from "./ProjectSettings";
import { errorText } from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import styles from "../styles/project.module.css";

const cx = scoped(base, controls, styles);
const taskStatus: Record<string, string> = {
  draft: "草稿",
  ready: "待推进",
  active: "正在推进",
  waiting: "等待条件",
  paused: "已暂停",
  completed: "已验收",
  canceled: "已取消",
};

/** The project landing page uses durable goals and evidence, not message recency. */
export function ProjectOverview({
  workspace,
  workspaceID,
  settings,
  onWorkspaceChange,
  onOpenTask,
  onNewTask,
}: {
  workspace: Workspace;
  workspaceID: string;
  settings?: Settings;
  onWorkspaceChange: (workspace: Workspace) => void;
  onOpenTask: (taskID: string) => void;
  onNewTask: () => void;
}) {
  const [value, setValue] = useState<Overview>();
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    try {
      setValue(
        await api<Overview>(`/api/v1/workspaces/${workspaceID}/overview`),
      );
      setError("");
    } catch (failure) {
      setError(errorText(failure));
    }
  }, [workspaceID]);
  useEffect(() => {
    setValue(undefined);
    void load();
  }, [load]);
  if (error)
    return (
      <div className={cx("project-home")} role="alert">
        <p>{error}</p>
        <button onClick={() => void load()}>重新加载项目</button>
      </div>
    );
  if (!value)
    return (
      <div className={cx("project-home")} role="status">
        正在读取项目状态…
      </div>
    );
  const tasks = value.tasks ?? [];
  const active = tasks.filter(
    (item) => !["completed", "canceled"].includes(item.task.status),
  );
  const completed = tasks.filter((item) => item.task.status === "completed");
  const goals = (value.records ?? []).filter((record) =>
    ["goal", "decision", "constraint", "rule"].includes(record.kind),
  );
  return (
    <div className={cx("project-home")}>
      <header className={cx("project-heading")}>
        <div>
          <span className={cx("eyebrow")}>项目概览</span>
          <h1>{workspace.name}</h1>
          <p>
            {goals[0]?.content ||
              workspace.description ||
              "直接描述下一个目标，项目会保存要求、进展和验收证据。"}
          </p>
        </div>
        <button className={cx("primary")} onClick={onNewTask}>
          新任务
        </button>
      </header>
      <ProjectSettings
        workspace={workspace}
        settings={settings}
        onWorkspaceChange={(updated) => {
          onWorkspaceChange(updated);
          setValue((current) => current ? { ...current, workspace: updated } : current);
        }}
      />
      {!!value.needs_attention?.length && (
        <section aria-labelledby="project-attention">
          <h2 id="project-attention">需要你处理</h2>
          <div className={cx("project-list")}>
            {value.needs_attention.map((question) => (
              <button
                className={cx("project-row attention")}
                key={question.id}
                onClick={() => {
                  const task = tasks.find(
                    (item) => item.task.id === question.task_id,
                  );
                  if (task) onOpenTask(task.task.id);
                }}
              >
                <strong>{question.body}</strong>
                <span>{question.blocking_scope || "需要你的决定"}</span>
              </button>
            ))}
          </div>
        </section>
      )}
      <section aria-labelledby="project-active">
        <h2 id="project-active">正在推进</h2>
        {active.length ? (
          <div className={cx("project-list")}>
            {active.map((item) => (
              <button
                className={cx("project-row")}
                key={item.task.id}
                onClick={() => onOpenTask(item.task.id)}
              >
                <strong>{item.revision.goal}</strong>
                <span>
                  {taskStatus[item.task.status] || item.task.status}
                  {item.task.waiting_reason
                    ? ` · ${item.task.waiting_reason}`
                    : ""}
                  {item.task.evaluation_status === "needs_revalidation"
                    ? " · 当前结果待复验"
                    : ""}
                </span>
              </button>
            ))}
          </div>
        ) : (
          <p className={cx("project-empty")}>
            暂无进行中的任务。新目标会在原对话中持续保存，交付后仍需独立验收。
          </p>
        )}
      </section>
      {!!completed.length && (
        <section aria-labelledby="project-outcomes">
          <h2 id="project-outcomes">最近成果</h2>
          <div className={cx("project-list")}>
            {completed.slice(0, 5).map((item) => (
              <button
                className={cx("project-row")}
                key={item.task.id}
                onClick={() => onOpenTask(item.task.id)}
              >
                <strong>{item.revision.goal}</strong>
                <span>
                  目标 v{item.task.current_revision} · 当前验收
                  {item.task.evaluation_status === "current" ? "有效" : "待复验"}
                </span>
              </button>
            ))}
          </div>
        </section>
      )}
      <section aria-labelledby="project-all">
        <h2 id="project-all">全部任务 · 项目资料</h2>
        <details>
          <summary>{tasks.length} 个持久任务</summary>
          <div className={cx("project-list")}>
            {tasks.map((item) => (
              <button
                className={cx("project-row")}
                key={item.task.id}
                onClick={() => onOpenTask(item.task.id)}
              >
                <strong>{item.revision.goal}</strong>
                <span>
                  v{item.task.current_revision} ·{" "}
                  {taskStatus[item.task.status] || item.task.status}
                </span>
              </button>
            ))}
          </div>
        </details>
        <details>
          <summary>{value.records?.length ?? 0} 条有效项目记录</summary>
          {(value.records ?? []).map((record) => (
            <article className={cx("project-record")} key={record.id}>
              <strong>{record.content}</strong>
              <span>
                {record.kind} · v{record.revision} · {record.applicability}
              </span>
            </article>
          ))}
        </details>
      </section>
    </div>
  );
}
