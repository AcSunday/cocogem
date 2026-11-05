import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  statusText,
  type Agent,
  type Artifact,
  type BudgetPolicy,
  type Claim,
  type CollaborationMessage,
  type CriterionResult,
  type DatasetManifest,
  type InboxItem,
  type ProjectTaskView,
  type Question,
  type Settings,
  type Source,
  type TaskRevision,
  type ThreadView,
  type Wakeup,
  type Workspace,
} from "../api/client";
import { errorText, formatDateTime } from "../lib/presentation";
import { ProjectSettings } from "./ProjectSettings";
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

type Snapshot = {
  view: ProjectTaskView;
  thread: ThreadView;
  artifacts: Artifact[];
  questions: Question[];
  inbox: InboxItem[];
  revisions: TaskRevision[];
  claims: Claim[];
  datasets: DatasetManifest[];
  messages: CollaborationMessage[];
  wakeups: Wakeup[];
};

const fields: [keyof BudgetPolicy, string, string][] = [
  ["auto_continuations", "自动续办", "次"],
  ["parallel_branches", "并行分支", "个"],
  ["writer_branches", "写分支", "个"],
  ["fix_rounds", "修复轮次", "轮"],
  ["no_progress_failures", "连续无进展", "次"],
];

const artifactRef = (item: Artifact) => `artifact:${item.id}@v${item.version}`;
const sourceRef = (item: Source) => `source:${item.id}`;

/** A durable task page: every mutation is recorded by the project API. */
export function TaskDetail({
  taskID,
  agents,
  workspace,
  settings,
  onWorkspaceChange,
  onOpenThread,
  onOpenArtifact,
  onOpenSource,
}: {
  taskID: string;
  agents: Agent[];
  workspace?: Workspace;
  settings?: Settings;
  onWorkspaceChange?: (workspace: Workspace) => void;
  onOpenThread: (threadID: string) => void;
  onOpenArtifact: (artifact: Artifact) => void;
  onOpenSource: (source: Source) => void;
}) {
  const [snapshot, setSnapshot] = useState<Snapshot>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [goal, setGoal] = useState("");
  const [steering, setSteering] = useState("");
  const [policy, setPolicy] = useState("next_boundary");
  const [budget, setBudget] = useState<BudgetPolicy>();
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const [candidate, setCandidate] = useState("");
  const [evidence, setEvidence] = useState<Record<string, string[]>>({});
  const [results, setResults] = useState<Record<string, string>>({});
  const goalDirty = useRef(false);
  const budgetDirty = useRef(false);

  const load = useCallback(async () => {
    try {
      const view = await api<ProjectTaskView>(`/api/v1/tasks/${taskID}`);
      const [thread, artifactPage, questionPage, inboxPage, revisionPage, claimPage, datasetPage, messagePage, wakeupPage] =
        await Promise.all([
          api<ThreadView>(`/api/v1/threads/${view.task.thread_id}`),
          api<{ items: Artifact[] }>(`/api/v1/threads/${view.task.thread_id}/artifacts`),
          api<{ items: Question[] }>(`/api/v1/tasks/${taskID}/questions`),
          api<{ items: InboxItem[] }>(`/api/v1/tasks/${taskID}/inbox`),
          api<{ items: TaskRevision[] }>(`/api/v1/tasks/${taskID}/revisions`),
          api<{ items: Claim[] }>(`/api/v1/tasks/${taskID}/claims`),
          api<{ items: DatasetManifest[] }>(`/api/v1/tasks/${taskID}/datasets`),
          api<{ items: CollaborationMessage[] }>(`/api/v1/tasks/${taskID}/messages`),
          api<{ items: Wakeup[] }>(`/api/v1/tasks/${taskID}/wakeups`),
        ]);
      const artifacts = artifactPage.items ?? [];
      setSnapshot({
        view,
        thread,
        artifacts,
        questions: questionPage.items ?? [],
        inbox: inboxPage.items ?? [],
        revisions: revisionPage.items ?? [],
        claims: claimPage.items ?? [],
        datasets: datasetPage.items ?? [],
        messages: messagePage.items ?? [],
        wakeups: wakeupPage.items ?? [],
      });
      if (!goalDirty.current) setGoal(view.revision.goal);
      if (!budgetDirty.current) setBudget(view.budget.policy);
      setCandidate((current) =>
        artifacts.some((item) => artifactRef(item) === current)
          ? current
          : artifacts.length
            ? artifactRef(artifacts[artifacts.length - 1])
            : "",
      );
      setError("");
    } catch (failure) {
      setError(errorText(failure));
    }
  }, [taskID]);

  useEffect(() => {
    goalDirty.current = false;
    budgetDirty.current = false;
    setSnapshot(undefined);
    void load();
  }, [load]);
  useEffect(() => {
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void load();
    }, 3000);
    return () => window.clearInterval(timer);
  }, [load]);

  const mutate = async (name: string, action: () => Promise<unknown>) => {
    if (busy) return;
    setBusy(name);
    setError("");
    try {
      await action();
      await load();
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setBusy("");
    }
  };

  if (!snapshot)
    return (
      <div className={cx("project-home")} role={error ? "alert" : "status"}>
        <p>{error || "正在读取任务、执行记录和验收状态…"}</p>
        {error && <button onClick={() => void load()}>重新加载任务</button>}
      </div>
    );

  const { view, artifacts, questions, inbox, revisions, claims, datasets, messages, wakeups } = snapshot;
  const { task, revision, budget: account } = view;
  const completions = view.completions ?? [];
  const criteria = revision.acceptance ?? [];
  const sources = snapshot.thread.sources ?? [];
  const runs = snapshot.thread.runs ?? [];
  const latestRun = runs[runs.length - 1];
  const running = !!latestRun && ["queued", "running", "canceling"].includes(latestRun.status);
  const remaining = account.policy.auto_continuations - account.used_continuations - account.reserved_runs;
  const member = (id: string) => agents.find((agent) => agent.id === id)?.display_name || id;
  const selectedArtifact = artifacts.find((item) => artifactRef(item) === candidate);
  const evidenceChoices = [
    ...artifacts.map((item) => ({ ref: artifactRef(item), label: `${item.title} v${item.version}` })),
    ...sources.map((item) => ({ ref: sourceRef(item), label: item.title })),
  ];

  return (
    <div className={cx("project-home task-detail")}>
      <header className={cx("project-heading")}>
        <div>
          <span className={cx("eyebrow")}>任务 v{task.current_revision} · {taskStatus[task.status] || task.status}</span>
          <h1>{revision.goal}</h1>
          <p>
            {running
              ? `当前执行：${statusText[latestRun.status] || latestRun.status}`
              : task.waiting_reason || "当前没有正在执行的步骤；目标验收与运行成功分别记录。"}
          </p>
        </div>
        <button className={cx("primary")} onClick={() => onOpenThread(task.thread_id)}>
          打开执行对话
        </button>
      </header>
      {error && <p className={cx("error")} role="alert">{error}</p>}

      {workspace && onWorkspaceChange && (
        <section aria-labelledby="task-project-permissions">
          <h2 id="task-project-permissions">项目权限</h2>
          <p className={cx("section-note")}>
            项目级执行模式、持续授权和以后新建根任务的预算默认值。Run 仍按冻结的授权快照执行；切换模式不会重置本任务预算。
          </p>
          <ProjectSettings
            workspace={workspace}
            settings={settings}
            onWorkspaceChange={onWorkspaceChange}
          />
        </section>
      )}

      <section aria-labelledby="task-next">
        <h2 id="task-next">下一步</h2>
        <p className={cx("project-empty")}>
          {task.status === "completed"
            ? "当前目标版本已通过所记录的人工验收；新的目标或证据变化会要求复验。"
            : task.status === "paused"
              ? "任务已暂停，恢复后才能接受新的执行输入。"
              : running
                ? "可以保存补充；平台会在持久执行边界更新目标，不把已收到误报为模型已理解。"
                : "打开执行对话继续推进。已有产物不会自动替代验收。"}
        </p>
        <div className={cx("task-actions")}>
          {task.status === "paused" ? (
            <button disabled={!!busy} onClick={() => void mutate("resume", () => api(`/api/v1/tasks/${taskID}/actions`, "POST", { action: "resume", request_id: crypto.randomUUID() }))}>恢复任务</button>
          ) : !["completed", "canceled"].includes(task.status) && (
            <button disabled={!!busy} onClick={() => void mutate("pause", () => api(`/api/v1/tasks/${taskID}/actions`, "POST", { action: "pause", request_id: crypto.randomUUID() }))}>暂停任务</button>
          )}
          {task.status === "ready" && !running && (
            <button disabled={!!busy} onClick={() => void mutate("start", () => api(`/api/v1/tasks/${taskID}/actions`, "POST", { action: "start", request_id: crypto.randomUUID() }))}>标记开始</button>
          )}
        </div>
      </section>

      <section aria-labelledby="task-steering">
        <h2 id="task-steering">补充与纠偏</h2>
        <form className={cx("task-form")} onSubmit={(event) => {
          event.preventDefault();
          const body = steering.trim();
          if (!body) return;
          void mutate("steering", async () => {
            const item = await api<InboxItem>(`/api/v1/tasks/${taskID}/inbox`, "POST", {
              source_message_id: crypto.randomUUID(), expected_revision: task.current_revision,
              kind: "supplement", body, application_policy: policy, idempotency_key: crypto.randomUUID(),
            });
            if (policy === "interrupt" && running) {
              await api(`/api/v1/runs/${latestRun.id}/cancel`, "POST", {});
            }
            setSteering("");
            return item;
          });
        }}>
          <label>补充内容
            <textarea value={steering} onChange={(event) => setSteering(event.target.value)} placeholder="例如：保留 24 小时，不能新增 Redis" required disabled={task.status === "paused" || task.status === "canceled"} />
          </label>
          <div className={cx("inline-form")}>
            <label>应用方式
              <select value={policy} onChange={(event) => setPolicy(event.target.value)}>
                <option value="next_boundary">下一个执行边界</option>
                <option value="interrupt" disabled={!running}>立即打断并在下次执行应用</option>
                <option value="follow_up">加入后续任务</option>
              </select>
            </label>
            <button className={cx("primary")} disabled={!!busy || !steering.trim() || ["paused", "canceled"].includes(task.status)}>保存补充</button>
          </div>
        </form>
        {!!inbox.length && <div className={cx("record-list")}>
          {[...inbox].reverse().map((item) => <article className={cx("project-record")} key={item.id}>
            <strong>{item.body}</strong>
            <span>{item.application_policy === "interrupt" ? "立即打断" : item.application_policy === "follow_up" ? "后续任务" : "下一边界"} · {item.status === "applied" ? `已应用到目标 v${item.applied_revision}` : item.status === "pending" ? "已收到 · 待应用" : item.status} · {formatDateTime(item.created_at)}</span>
          </article>)}
        </div>}
      </section>

      {!!questions.length && <section aria-labelledby="task-questions">
        <h2 id="task-questions">问题与决定</h2>
        {questions.map((question) => <article className={cx("question-card")} key={question.id}>
          <strong>{question.body}</strong>
          <small>{question.blocking_scope} · 目标 v{question.task_revision}</small>
          {question.status === "open" ? <>
            {!!question.options?.length && <div className={cx("task-actions")}>{question.options.map((option) => <button key={option} className={cx("small")} onClick={() => setAnswers((current) => ({ ...current, [question.id]: option }))}>{option}{option === question.recommended ? "（建议）" : ""}</button>)}</div>}
            <label>回答<textarea value={answers[question.id] ?? question.recommended ?? ""} onChange={(event) => setAnswers((current) => ({ ...current, [question.id]: event.target.value }))} /></label>
            <button className={cx("primary")} disabled={!!busy || !(answers[question.id] ?? question.recommended ?? "").trim()} onClick={() => void mutate(`answer-${question.id}`, () => api(`/api/v1/tasks/${taskID}/questions/${question.id}/answers`, "POST", { answer: answers[question.id] ?? question.recommended, answer_ref: `user-answer:${crypto.randomUUID()}` }, { "If-Match": String(task.current_revision) }))}>提交回答</button>
          </> : <p>{question.answer}<small> · 已解决 {question.resolved_at ? formatDateTime(question.resolved_at) : ""}</small></p>}
        </article>)}
      </section>}

      <section aria-labelledby="task-budget">
        <h2 id="task-budget">本任务协作预算</h2>
        <p className={cx("section-note")}>已用 {account.used_continuations} 次 · 预留 {account.reserved_runs} 次 · 剩余 {remaining} 次。只修改此根任务共享账户，不清空使用记录。</p>
        {budget && <form onSubmit={(event) => {
          event.preventDefault();
          void mutate("budget", async () => {
            await api(`/api/v1/tasks/${taskID}/budget`, "PATCH", { policy: budget }, { "If-Match": String(account.revision) });
            budgetDirty.current = false;
          });
        }}>
          <div className={cx("budget-grid")}>{fields.map(([key, label, unit]) => <label key={key}>{label}<span><input type="number" required min={1} value={budget[key]} onChange={(event) => {
            budgetDirty.current = true;
            setBudget({ ...budget, [key]: Number(event.target.value) });
          }} />{unit}</span></label>)}</div>
          <div className={cx("task-actions")}>
            <button className={cx("primary")} disabled={!!busy}>{task.waiting_reason === "budget" ? "调整预算并继续" : "保存并用于后续准入"}</button>
            {task.waiting_reason === "budget" && <button type="button" disabled={!!busy} onClick={() => void mutate("keep-paused", () => api(`/api/v1/tasks/${taskID}/actions`, "POST", { action: "pause", request_id: crypto.randomUUID() }))}>保持暂停</button>}
          </div>
        </form>}
      </section>

      <section aria-labelledby="task-acceptance">
        <h2 id="task-acceptance">当前目标验收</h2>
        <p className={cx("section-note")}>验收绑定目标 v{task.current_revision}、评估代次 {task.evaluation_generation}、确切候选和证据。这里的人工判断不会伪装成宿主自动测试。</p>
        <form className={cx("task-form")} onSubmit={(event) => {
          event.preventDefault();
          if (!selectedArtifact) return;
          void mutate("completion", async () => {
            const criterionResults: CriterionResult[] = criteria.map((criterion) => ({
              criterion_id: criterion.criterion_id,
              result: results[criterion.criterion_id] || "inconclusive",
              evidence: evidence[criterion.criterion_id] ?? [],
              limitations: [],
            }));
            await api(`/api/v1/tasks/${taskID}/completions`, "POST", {
              evaluation_generation: task.evaluation_generation,
              input_snapshot_digest: revision.digest,
              candidate_refs: [candidate],
              results: criterionResults,
              judge_member_id: "user",
              evidence_producer: "human_confirmed",
              unresolved: criterionResults.some((item) => item.result !== "passed") ? ["仍有必需验收未通过或未核实"] : [],
            });
          });
        }}>
          <label>候选成果
            <select value={candidate} onChange={(event) => setCandidate(event.target.value)} required>
              <option value="">请选择确切产物版本</option>
              {artifacts.map((item) => <option key={artifactRef(item)} value={artifactRef(item)}>{item.title} · v{item.version}</option>)}
            </select>
          </label>
          {criteria.map((criterion) => <fieldset className={cx("criterion")} key={criterion.criterion_id}>
            <legend>{criterion.statement}{criterion.required ? "（必需）" : ""}</legend>
            <label>判断<select value={results[criterion.criterion_id] || "inconclusive"} onChange={(event) => setResults((current) => ({ ...current, [criterion.criterion_id]: event.target.value }))}>
              <option value="inconclusive">无法确认</option><option value="passed">通过</option><option value="failed">未通过</option><option value="not_run">未验证</option><option value="not_applicable">不适用</option>
            </select></label>
            <span className={cx("section-note")}>证据（通过时至少选择一项）</span>
            {evidenceChoices.map((choice) => <label className={cx("check-label")} key={`${criterion.criterion_id}-${choice.ref}`}><input type="checkbox" checked={(evidence[criterion.criterion_id] ?? []).includes(choice.ref)} onChange={(event) => setEvidence((current) => {
              const previous = current[criterion.criterion_id] ?? [];
              return { ...current, [criterion.criterion_id]: event.target.checked ? [...previous, choice.ref] : previous.filter((ref) => ref !== choice.ref) };
            })} />{choice.label}</label>)}
          </fieldset>)}
          <button className={cx("primary")} disabled={!!busy || !selectedArtifact}>记录人工验收</button>
        </form>
        {!!completions.length && <div className={cx("record-list")}>{[...completions].reverse().map((item) => <article className={cx("project-record")} key={item.id}><strong>{item.validity === "current" && item.task_revision === task.current_revision ? "当前版本验收记录" : `历史验收 · 目标 v${item.task_revision}`}</strong><span>{item.evidence_producer} · {(item.results ?? []).map((result) => `${result.criterion_id}: ${result.result}`).join(" · ")} · {formatDateTime(item.created_at)}</span></article>)}</div>}
      </section>

      <section aria-labelledby="task-artifacts">
        <h2 id="task-artifacts">成果与来源</h2>
        {artifacts.length ? <div className={cx("project-list")}>{artifacts.map((item) => <button className={cx("project-row")} key={artifactRef(item)} onClick={() => onOpenArtifact(item)}><strong>{item.title} · v{item.version}</strong><span>{item.type} · {formatDateTime(item.created_at)}</span></button>)}</div> : <p className={cx("project-empty")}>尚无持久成果。</p>}
        {!!sources.length && <div className={cx("record-list")}>{sources.map((item) => <button className={cx("project-row")} key={item.id} onClick={() => onOpenSource(item)}><strong>{item.title}</strong><span>{item.kind} · {item.url || item.path || item.source_locator}</span></button>)}</div>}
      </section>

      <section aria-labelledby="task-plan">
        <h2 id="task-plan">目标、计划与验证记录</h2>
        <details>
          <summary>编辑目标（生成 v{task.current_revision + 1}，原版本保留）</summary>
          <form className={cx("task-form")} onSubmit={(event) => {
            event.preventDefault();
            void mutate("revision", async () => {
              await api(`/api/v1/tasks/${taskID}/revisions`, "POST", {
                goal, scope: revision.scope ?? [], non_goals: revision.non_goals ?? [], constraint_refs: revision.constraint_refs ?? [],
                acceptance: revision.acceptance, input_refs: revision.input_refs ?? [], allowed_actions: revision.allowed_actions ?? [],
                source_message_id: crypto.randomUUID(), accepted_by: "user",
              }, { "If-Match": String(task.current_revision) });
              goalDirty.current = false;
            });
          }}><label>目标<textarea required value={goal} onChange={(event) => {
            goalDirty.current = true;
            setGoal(event.target.value);
          }} /></label><button className={cx("primary")} disabled={!!busy || goal.trim() === revision.goal.trim()}>保存新目标版本</button></form>
        </details>
        <details><summary>{revisions.length} 个目标版本</summary>{revisions.map((item) => <article className={cx("project-record")} key={item.revision}><strong>v{item.revision} · {item.goal}</strong><span>{item.accepted_by} · {formatDateTime(item.created_at)} · {item.digest}</span></article>)}</details>
        <details><summary>{runs.length} 次执行记录</summary>{runs.map((run) => <article className={cx("project-record")} key={run.id}><strong>{statusText[run.status] || run.status} · 目标 v{run.task_revision || 1}</strong><span>{run.effective_execution_mode || "controlled"} · {run.error || run.id} · {formatDateTime(run.created_at)}</span></article>)}</details>
        <details><summary>{messages.length} 条任务协作消息</summary>{messages.map((item) => <article className={cx("project-record")} key={item.id}><strong>{item.kind} · {item.body}</strong><span>{member(item.sender_member_id)} · 目标任务 {item.target_task_id} · {formatDateTime(item.created_at)}</span></article>)}</details>
        <details><summary>{wakeups.length} 条等待与唤醒记录</summary>{wakeups.map((item) => <article className={cx("project-record")} key={item.id}><strong>{item.predicate}</strong><span>{item.status} · {item.wake_source}/{item.event_id} · 代次 {item.wait_generation}</span></article>)}</details>
      </section>

      {(!!claims.length || !!datasets.length) && <section aria-labelledby="task-research"><h2 id="task-research">研究记录</h2>
        {claims.map((item) => <article className={cx("project-record")} key={`${item.id}-${item.revision}`}><strong>{item.statement}</strong><span>{item.status} · 支持 {item.supporting_refs?.length ?? 0} · 反证 {item.contradicting_refs?.length ?? 0} · {item.limitations?.join("；")}</span></article>)}
        {datasets.map((item) => <article className={cx("project-record")} key={`${item.id}-${item.revision}`}><strong>{item.schema}</strong><span>{item.time_range} · {item.location} · 缺失规则：{item.missing_rules}</span></article>)}
      </section>}
    </div>
  );
}
