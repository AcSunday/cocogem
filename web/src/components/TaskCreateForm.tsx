import { useEffect, useRef, useState } from "react";
import { api, type Agent, type ProjectTaskView, type Run } from "../api/client";
import { t, useLanguage } from "../lib/i18n";
import { errorText } from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import styles from "../styles/project.module.css";
import { Select } from "./Select";

const cx = scoped(base, controls, styles);

export function TaskCreateForm({
  workspaces,
  workspaceID = "",
  parentID,
  threadID,
  initialGoal = "",
  onCreated,
}: {
  workspaces: Record<string, string>;
  workspaceID?: string;
  parentID?: string;
  threadID?: string;
  initialGoal?: string;
  onCreated: (task: ProjectTaskView, run?: Run) => void;
}) {
  useLanguage();
  const [workspace, setWorkspace] = useState(
    workspaceID || Object.keys(workspaces)[0] || "",
  );
  const [members, setMembers] = useState<Agent[]>([]);
  const [owner, setOwner] = useState("");
  const [title, setTitle] = useState(initialGoal.split("\n")[0].slice(0, 100));
  const [goal, setGoal] = useState(initialGoal);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState<ProjectTaskView>();
  const requestID = useRef(crypto.randomUUID());
  useEffect(() => {
    let canceled = false;
    setMembers([]);
    setOwner("");
    if (workspace)
      void api<{ items: Agent[] }>(`/api/v1/workspaces/${workspace}/agents`)
        .then((page) => {
          if (!canceled)
            setMembers(
              (page.items ?? []).filter((member) => !member.disabled),
            );
        })
        .catch((failure) => {
          if (!canceled) setError(errorText(failure));
        });
    return () => {
      canceled = true;
    };
  }, [workspace]);
  const submit = async (start: boolean) => {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      const task =
        saved ??
        (await api<ProjectTaskView>(
          `/api/v1/workspaces/${workspace}/tasks`,
          "POST",
          {
            request_id: requestID.current,
            title: title.trim(),
            goal: goal.trim() || title.trim(),
            owner_member_id: owner,
            parent_id: parentID,
            thread_id: threadID,
          },
        ));
      setSaved(task);
      const run = start
        ? await api<Run>(`/api/v1/tasks/${task.task.id}/start`, "POST", {})
        : undefined;
      onCreated(task, run);
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form
      className={cx("task-form task-create-form")}
      onSubmit={(event) => {
        event.preventDefault();
        void submit(false);
      }}
    >
      <p className={cx("section-note")}>
        {parentID
          ? t(
              "子任务保留父任务关联，并共享协作预算。",
              "Subtasks remain linked to their parent task and share its collaboration budget.",
            )
          : threadID
            ? t(
                "把当前讨论整理成任务，继续使用这个会话。",
                "Turn the current discussion into a task while continuing in this conversation.",
              )
            : t(
                "先保存目标，准备好后再开始执行。",
                "Save the objective first, or create and start execution right away.",
              )}
      </p>
      <label>
        {t("任务标题", "Task title")}
        <input
          autoFocus
          required
          maxLength={150}
          value={title}
          disabled={busy || !!saved}
          onChange={(event) => setTitle(event.target.value)}
          placeholder={t(
            "例如：修复会话导航高亮",
            "e.g., Fix conversation navigation highlight",
          )}
        />
      </label>
      <label>
        {t("目标与交付要求", "Goal & delivery requirements")}
        <textarea
          value={goal}
          disabled={busy || !!saved}
          maxLength={16000}
          onChange={(event) => setGoal(event.target.value)}
          placeholder={t(
            "描述期望结果、必要约束和验收要求…",
            "Describe the expected outcome, constraints, and acceptance criteria…",
          )}
          rows={5}
        />
      </label>
      <div className={cx("task-create-properties")}>
        <label>
          {t("所属工作空间", "Project")}
          <Select
            required
            value={workspace}
            disabled={busy || !!saved || !!parentID || !!threadID}
            onChange={setWorkspace}
          >
            {Object.entries(workspaces).map(([id, name]) => (
              <option key={id} value={id}>
                {name}
              </option>
            ))}
          </Select>
        </label>
        <label>
          {t("任务负责人", "Task owner")}
          <Select
            value={owner}
            disabled={busy || !!saved || !!threadID}
            onChange={setOwner}
          >
            <option value="">
              {t("沿用空间默认成员", "Use project default member")}
            </option>
            {members.map((member) => (
              <option key={member.id} value={member.id}>
                {member.display_name}
              </option>
            ))}
          </Select>
        </label>
      </div>
      {error && (
        <p role="alert" className={cx("error")}>
          {error}
          {saved &&
            t(
              "；任务已保存，可以重试开始或查看任务。",
              "; the task has been saved — you can retry starting it or view the task.",
            )}
        </p>
      )}
      <div className={cx("task-actions")}>
        <button type="submit" disabled={busy || !workspace || !title.trim()}>
          {saved
            ? t("查看已保存任务", "View saved task")
            : t("保存任务", "Save task")}
        </button>
        <button
          type="button"
          className={cx("primary")}
          disabled={busy || !workspace || !title.trim()}
          onClick={() => void submit(true)}
        >
          {busy
            ? t("正在保存…", "Saving…")
            : saved
              ? t("重试开始", "Retry start")
              : t("创建并开始", "Create & start")}
        </button>
      </div>
    </form>
  );
}
