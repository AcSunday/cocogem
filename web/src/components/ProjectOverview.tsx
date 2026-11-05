import { useCallback, useEffect, useState } from "react";
import { api } from "../api/client";
import type { ProjectOverview as Overview, Workspace } from "../api/client";
import { TaskTodoList } from "./TaskTodoList";
import { errorText } from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import styles from "../styles/project.module.css";

const cx = scoped(base, controls, styles);

/** The project landing page uses durable goals and evidence, not message recency. */
export function ProjectOverview({
  workspace,
  workspaceID,
  onOpenTask,
  onNewTask,
  onEditWorkspace,
}: {
  workspace: Workspace;
  workspaceID: string;
  onOpenTask: (taskID: string) => void;
  onNewTask: () => void;
  onEditWorkspace: () => void;
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
  useEffect(() => {
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void load();
    }, 5000);
    return () => window.clearInterval(timer);
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
  const completed = tasks.filter((item) => item.task.status === "completed");
  const goals = (value.records ?? []).filter((record) =>
    ["goal", "decision", "constraint", "rule"].includes(record.kind),
  );
  return (
    <div className={cx("project-home")}>
      <header className={cx("project-heading")}>
        <div>
          <span className={cx("eyebrow")}>任务清单</span>
          <h1>{workspace.name}</h1>
          <p>
            {goals[0]?.content ||
              workspace.description ||
              "直接描述下一个目标，项目会保存要求、进展和验收证据。"}
          </p>
        </div>
        <div className={cx("project-actions")}>
          <button onClick={onEditWorkspace}>编辑项目</button>
          <button className={cx("primary")} onClick={onNewTask}>
            新任务
          </button>
        </div>
      </header>
      <TaskTodoList
        tasks={tasks}
        questions={value.needs_attention ?? []}
        onOpenTask={onOpenTask}
        onChanged={load}
      />
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
      <section aria-labelledby="project-records">
        <h2 id="project-records">项目资料</h2>
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
