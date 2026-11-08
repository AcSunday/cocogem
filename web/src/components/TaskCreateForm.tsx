import { useEffect, useRef, useState } from "react";
import { api, type Agent, type ProjectTaskView, type Run } from "../api/client";
import { errorText } from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import styles from "../styles/project.module.css";

const cx = scoped(base, controls, styles);

export function TaskCreateForm({ workspaces, workspaceID = "", parentID, threadID, initialGoal = "", onCreated }: {
  workspaces: Record<string, string>;
  workspaceID?: string;
  parentID?: string;
  threadID?: string;
  initialGoal?: string;
  onCreated: (task: ProjectTaskView, run?: Run) => void;
}) {
  const [workspace, setWorkspace] = useState(workspaceID || Object.keys(workspaces)[0] || "");
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
    setMembers([]); setOwner("");
    if (workspace) void api<{ items: Agent[] }>(`/api/v1/workspaces/${workspace}/agents`).then((page) => {
      if (!canceled) setMembers((page.items ?? []).filter((member) => !member.disabled));
    }).catch((failure) => { if (!canceled) setError(errorText(failure)); });
    return () => { canceled = true; };
  }, [workspace]);
  const submit = async (start: boolean) => {
    if (busy) return;
    setBusy(true); setError("");
    try {
      const task = saved ?? await api<ProjectTaskView>(`/api/v1/workspaces/${workspace}/tasks`, "POST", {
        request_id: requestID.current, title: title.trim(), goal: goal.trim() || title.trim(),
        owner_member_id: owner, parent_id: parentID, thread_id: threadID,
      });
      setSaved(task);
      const run = start ? await api<Run>(`/api/v1/tasks/${task.task.id}/start`, "POST", {}) : undefined;
      onCreated(task, run);
    } catch (failure) { setError(errorText(failure)); }
    finally { setBusy(false); }
  };
  return <form className={cx("task-form task-create-form")} onSubmit={(event) => { event.preventDefault(); void submit(false); }}>
    <p className={cx("section-note")}>{parentID ? "子任务保留父任务关联，并共享协作预算。" : threadID ? "把当前讨论整理成任务，继续使用这个会话。" : "先保存目标，准备好后再开始执行。"}</p>
    <label>任务标题<input autoFocus required maxLength={150} value={title} disabled={busy || !!saved} onChange={(event) => setTitle(event.target.value)} placeholder="例如：修复会话导航高亮" /></label>
    <label>目标与交付要求<textarea value={goal} disabled={busy || !!saved} maxLength={16000} onChange={(event) => setGoal(event.target.value)} placeholder="描述期望结果、必要约束和验收要求…" rows={5} /></label>
    <div className={cx("task-create-properties")}>
      <label>所属工作空间<select required value={workspace} disabled={busy || !!saved || !!parentID || !!threadID} onChange={(event) => setWorkspace(event.target.value)}>{Object.entries(workspaces).map(([id, name]) => <option key={id} value={id}>{name}</option>)}</select></label>
      <label>任务负责人<select value={owner} disabled={busy || !!saved || !!threadID} onChange={(event) => setOwner(event.target.value)}><option value="">沿用空间默认成员</option>{members.map((member) => <option key={member.id} value={member.id}>{member.display_name}</option>)}</select></label>
    </div>
    {error && <p role="alert" className={cx("error")}>{error}{saved && "；任务已保存，可以重试开始或查看任务。"}</p>}
    <div className={cx("task-actions")}>
      <button type="submit" disabled={busy || !workspace || !title.trim()}>{saved ? "查看已保存任务" : "保存任务"}</button>
      <button type="button" className={cx("primary")} disabled={busy || !workspace || !title.trim()} onClick={() => void submit(true)}>{busy ? "正在保存…" : saved ? "重试开始" : "创建并开始"}</button>
    </div>
  </form>;
}
