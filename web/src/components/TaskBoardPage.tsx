import { useCallback, useEffect, useState } from "react";
import { api, type TaskBoard } from "../api/client";
import { errorText } from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import styles from "../styles/project.module.css";
import { TaskTodoList } from "./TaskTodoList";

const cx = scoped(base, controls, styles);

/** Global todo view: one task board across every workspace. */
export function TaskBoardPage({
  onOpenTask,
}: {
  onOpenTask: (workspaceID: string, taskID: string) => void;
}) {
  const [board, setBoard] = useState<TaskBoard>();
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    try {
      setBoard(await api<TaskBoard>("/api/v1/tasks"));
      setError("");
    } catch (failure) {
      setError(errorText(failure));
    }
  }, []);
  useEffect(() => {
    void load();
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void load();
    }, 5000);
    return () => window.clearInterval(timer);
  }, [load]);
  if (error)
    return (
      <div className={cx("project-home")} role="alert">
        <p>{error}</p>
        <button onClick={() => void load()}>重新加载任务</button>
      </div>
    );
  if (!board)
    return (
      <div className={cx("project-home")} role="status">
        正在读取全部工作空间的任务…
      </div>
    );
  return (
    <div className={cx("project-home task-board")}>
      <header className={cx("project-heading")}>
        <div>
          <span className={cx("eyebrow")}>全局视图</span>
          <h1>任务清单</h1>
          <p>跨越所有工作空间的持久任务；打开任务会切换到它所属的项目。</p>
        </div>
      </header>
      <TaskTodoList
        tasks={board.tasks ?? []}
        questions={board.needs_attention ?? []}
        workspaces={board.workspaces ?? undefined}
        onOpenTask={onOpenTask}
        onChanged={load}
      />
    </div>
  );
}
