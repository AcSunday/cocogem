import { MessageCard } from "../components/MessageCard";
import { WorkspaceForm } from "../components/forms/WorkspaceForm";
import { MembersForm } from "../components/forms/MembersForm";
import { ThreadForm } from "../components/forms/ThreadForm";
import {
  workflows,
  errorText,
  catIcon,
  memberLabel,
  defaultAssignments,
} from "../lib/presentation";
import { cx } from "../styles/classes";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import {
  api,
  APIError,
  setCSRF,
  terminal,
  statusText,
  mergeEvents,
  loadAllThreadPages,
  type Agent,
  type Artifact,
  type Event,
  type Routing,
  type Run,
  type RunInput,
  type Source,
  type Step,
  type System,
  type Thread,
  type ThreadView,
  type Workspace,
} from "../api/client";
import { Modal } from "../components/Modal";
import { Markdown } from "../components/Markdown";
import { ToolTimeline } from "../components/ToolTimeline";

const pathSelection = () => {
  const match = location.pathname.match(/^\/w\/([^/]+)(?:\/t\/([^/]+))?/);
  return { workspace: match?.[1] ?? "", thread: match?.[2] ?? "" };
};

export function App() {
  const [system, setSystem] = useState<System>();
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);
  const [workspaceID, setWorkspaceID] = useState(pathSelection().workspace);
  const [threadID, setThreadID] = useState(pathSelection().thread);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [threads, setThreads] = useState<Thread[]>([]);
  const [view, setView] = useState<ThreadView>();
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [steps, setSteps] = useState<Step[]>([]);
  const [events, setEvents] = useState<Event[]>([]);
  const [error, setError] = useState("");
  const [ready, setReady] = useState(false);
  const [busy, setBusy] = useState(false);
  const [modal, setModal] = useState<
    | "workspace"
    | "edit-workspace"
    | "agents"
    | "settings"
    | "thread"
    | "edit-thread"
    | "snapshot"
    | null
  >(null);
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [workflowChoices, setWorkflowChoices] = useState<
    Record<string, keyof typeof workflows>
  >({});
  const [selected, setSelected] = useState<string[]>([]);
  const [assignmentChoices, setAssignmentChoices] = useState<
    Record<string, Record<string, string>>
  >({});
  const [urls, setURLs] = useState("");
  const [scope, setScope] = useState("given_sources");
  const [routing, setRouting] = useState<Routing>();
  const [tab, setTab] = useState<"members" | "sources" | "artifacts" | "tools">(
    "members",
  );
  const [source, setSource] = useState<Source>();
  const [artifact, setArtifact] = useState<Artifact>();
  const [artifactRef, setArtifactRef] = useState<Artifact>();
  const [filter, setFilter] = useState("");
  const [connection, setConnection] = useState("");
  const [newMessages, setNewMessages] = useState(false);
  const [pendingRequest, setPendingRequest] = useState(false);
  const [navOpen, setNavOpen] = useState(false);
  const [verifyExecutor, setVerifyExecutor] = useState("claude");
  const [verifyModel, setVerifyModel] = useState("");
  const [verifying, setVerifying] = useState(false);
  const [verificationError, setVerificationError] = useState("");
  const timeline = useRef<HTMLDivElement>(null);
  const follows = useRef(true);
  const currentThread = useRef(threadID);
  const currentWorkspace = useRef(workspaceID);
  currentWorkspace.current = workspaceID;
  const refreshGeneration = useRef(0);
  const pendingSubmission = useRef(
    new Map<string, { input: RunInput; key: string; threadID: string }>(),
  );
  currentThread.current = threadID;
  const workspace = workspaces.find((item) => item.id === workspaceID);
  const [inspectedRunID, setInspectedRunID] = useState("");
  const currentRun = view?.runs.at(-1);
  const inspectedRun =
    view?.runs.find((run) => run.id === inspectedRunID) ?? currentRun;
  const active = view?.runs.some((run) => !terminal(run.status));
  const available = agents.filter((agent) => !agent.disabled);
  const draftKey = threadID || `new:${workspaceID}`;
  const draft = drafts[draftKey] ?? "";
  const threadMembers = view
    ? available.filter((agent) => view.thread.agent_ids?.includes(agent.id))
    : threadID
      ? []
      : available.slice(0, 4);
  const workflow =
    workflowChoices[draftKey] ??
    (view?.thread.workflow as keyof typeof workflows | undefined) ??
    "free";
  const setWorkflow = (value: keyof typeof workflows) =>
    setWorkflowChoices((current) => ({ ...current, [draftKey]: value }));
  const assignments: Record<string, string> = {
    ...defaultAssignments(threadMembers, view?.thread.coordinator_id),
    ...Object.fromEntries(
      Object.entries(assignmentChoices[draftKey] ?? {}).filter(([, id]) =>
        available.some((agent) => agent.id === id),
      ),
    ),
  };

  const navigate = useCallback((ws: string, th = "", push = true) => {
    setPendingRequest(pendingSubmission.current.has(th));
    setNavOpen(false);
    currentThread.current = th;
    currentWorkspace.current = ws;
    refreshGeneration.current++;
    setWorkspaceID(ws);
    setThreadID(th);
    setView(undefined);
    setEvents([]);
    setInspectedRunID("");
    setSteps([]);
    setArtifacts([]);
    setSource(undefined);
    setArtifact(undefined);
    setArtifactRef(undefined);
    setRouting(undefined);
    setSelected([]);
    if (push)
      history.pushState({}, "", ws ? `/w/${ws}${th ? `/t/${th}` : ""}` : "/");
  }, []);
  const loadSpaces = useCallback(async () => {
    const result = await api<{ items: Workspace[] }>("/api/v1/workspaces");
    setWorkspaces(result.items);
    return result.items;
  }, []);
  const loadMembers = useCallback(async (id: string) => {
    const result = await api<{ items: Agent[] }>(
      `/api/v1/workspaces/${id}/agents`,
    );
    if (currentWorkspace.current === id) setAgents(result.items);
  }, []);
  const loadThreads = useCallback(async (id: string) => {
    const items: Thread[] = [];
    let cursor = "";
    do {
      const result = await api<{ items: Thread[]; next_cursor: string }>(
        `/api/v1/workspaces/${id}/threads?limit=100&cursor=${cursor}`,
      );
      items.push(...result.items);
      cursor = result.next_cursor;
    } while (cursor);
    if (currentWorkspace.current === id) setThreads(items);
  }, []);
  const refreshThread = useCallback(async (id: string) => {
    const generation = ++refreshGeneration.current;
    const result = await loadAllThreadPages(id);
    const deliverables = await api<{ items: Artifact[] }>(
      `/api/v1/threads/${id}/artifacts`,
    );
    if (
      currentThread.current !== id ||
      generation !== refreshGeneration.current
    )
      return;
    setView(result);
    setArtifacts(deliverables.items);
    if (result.runs.length) {
      const run = await api<{ steps: Step[] }>(
        `/api/v1/runs/${result.runs.at(-1)!.id}`,
      );
      if (currentThread.current === id) setSteps(run.steps);
    }
  }, []);

  useEffect(() => {
    let canceled = false;
    async function initialize() {
      try {
        const params = new URLSearchParams(location.hash.slice(1));
        const token = params.get("bootstrap");
        if (token) {
          history.replaceState({}, "", location.pathname);
          await api("/bootstrap", "POST", { token });
        }
        const info = await api<System>("/api/v1/system");
        if (canceled) return;
        setSystem(info);
        setVerifyExecutor(
          info.executors?.find(
            (item) => item.installed && item.supported !== false,
          )?.id ?? "claude",
        );
        setCSRF(info.csrf_token);
        const spaces = await loadSpaces();
        if (!workspaceID && spaces.length) navigate(spaces[0].id);
        setReady(true);
      } catch (error) {
        if (!canceled) setError(errorText(error));
      }
    }
    void initialize();
    return () => {
      canceled = true;
    };
    // Bootstrap is a one-time mount operation, not a workspace-dependent request.
  }, []);
  useEffect(() => {
    const pop = () => {
      const next = pathSelection();
      navigate(next.workspace, next.thread, false);
    };
    addEventListener("popstate", pop);
    return () => removeEventListener("popstate", pop);
  }, []);
  useEffect(() => {
    if (ready && workspaceID) {
      void Promise.all([
        loadMembers(workspaceID),
        loadThreads(workspaceID),
      ]).catch((error) => setError(errorText(error)));
    }
  }, [ready, workspaceID, loadMembers, loadThreads]);
  useEffect(() => {
    if (ready && threadID) {
      setView(undefined);
      setEvents([]);
      void refreshThread(threadID).catch((error) => setError(errorText(error)));
    }
  }, [ready, threadID, refreshThread]);
  useEffect(() => {
    if (!inspectedRun) return;
    const id = inspectedRun.id;
    const thread = threadID;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const stream = new EventSource(`/api/v1/runs/${id}/events`);
    setConnection("正在连接");
    stream.onopen = () => setConnection("实时连接");
    stream.onmessage = (message) => {
      const event = JSON.parse(message.data) as Event;
      setEvents((current) => mergeEvents(current, event));
      if (!terminal(inspectedRun.status) && !timer)
        timer = setTimeout(() => {
          timer = undefined;
          void refreshThread(thread).catch((error) =>
            setError(errorText(error)),
          );
        }, 80);
      if (event.type === "run_finished") {
        stream.close();
        setConnection("");
        void refreshThread(thread);
        void loadThreads(workspaceID);
      }
    };
    stream.onerror = () => setConnection("连接中断，正在重新连接");
    return () => {
      stream.close();
      if (timer) clearTimeout(timer);
    };
  }, [
    inspectedRun?.id,
    inspectedRun?.status,
    threadID,
    workspaceID,
    refreshThread,
    loadThreads,
  ]);
  useEffect(() => {
    const element = timeline.current;
    if (!element) return;
    if (follows.current) {
      element.scrollTop = element.scrollHeight;
      setNewMessages(false);
    } else setNewMessages(true);
  }, [view?.messages.items]);

  async function createThread(title: string) {
    if (!workspaceID || available.length < 1)
      throw new Error("请先创建工作空间和可用成员");
    const thread = await api<Thread>(
      `/api/v1/workspaces/${workspaceID}/threads`,
      "POST",
      {
        title: title.slice(0, 40) || "新会话",
        agent_ids: available.slice(0, 4).map((agent) => agent.id),
        coordinator_id: available[Math.min(2, available.length - 1)].id,
        workflow,
      },
    );
    currentThread.current = thread.id;
    setWorkflowChoices((current) => ({ ...current, [thread.id]: workflow }));
    setAssignmentChoices((current) => ({
      ...current,
      [thread.id]: assignments,
    }));
    setDrafts((current) => ({
      ...current,
      [thread.id]: current[`new:${workspaceID}`] ?? title,
    }));
    setThreadID(thread.id);
    history.pushState({}, "", `/w/${workspaceID}/t/${thread.id}`);
    await loadThreads(workspaceID);
    return thread.id;
  }
  async function send(inputOverride?: RunInput) {
    if (busy || active) return;
    setBusy(true);
    setError("");
    setRouting(undefined);
    try {
      const text = inputOverride?.message ?? draft;
      if (!text.trim()) throw new Error("请输入问题");
      const id = threadID || (await createThread(text));
      let input: RunInput = inputOverride
        ? structuredClone(inputOverride)
        : {
            message: text,
            workflow: workflow === "free" ? "" : workflow,
            targets:
              workflow === "free" || workflow === "discussion" ? selected : [],
            assignments: workflow === "free" ? {} : assignments,
            source_urls: urls
              .split("\n")
              .map((s) => s.trim())
              .filter(Boolean),
            artifact_refs: artifactRef
              ? [{ id: artifactRef.id, version: artifactRef.version }]
              : [],
            research_scope: scope,
            proposal_id: "",
            retry_of_run_id: "",
          };
      let pending = pendingSubmission.current.get(id);
      if (
        pending &&
        pending.threadID === id &&
        pending.input.message !== input.message
      )
        throw new Error(
          "有一次提交结果未确认，请先重试原请求；新问题已保留为草稿。",
        );
      if (!pending || pending.threadID !== id) {
        input.proposal_id = "";
        const preview = await api<{ proposal_id?: string; routing: Routing }>(
          `/api/v1/threads/${id}/routing-preview`,
          "POST",
          input,
        );
        setRouting(preview.routing);
        if (!preview.proposal_id) return;
        input.proposal_id = preview.proposal_id;
        pending = {
          input: structuredClone(input),
          key: crypto.randomUUID(),
          threadID: id,
        };
        pendingSubmission.current.set(id, pending);
        setPendingRequest(true);
      } else {
        input = pending.input;
      }
      await api<Run>(`/api/v1/threads/${id}/runs`, "POST", input, {
        "Idempotency-Key": pending.key,
      });
      pendingSubmission.current.delete(id);
      setPendingRequest(false);
      setDrafts((current) => ({
        ...current,
        [draftKey]:
          current[draftKey] === input.message ? "" : current[draftKey],
        [id]: current[id] === input.message ? "" : current[id],
      }));
      setEvents([]);
      setInspectedRunID("");
      setArtifactRef(undefined);
      follows.current = true;
      await refreshThread(id);
      await loadThreads(workspaceID);
    } catch (error) {
      if (error instanceof APIError && error.status < 500) {
        pendingSubmission.current.delete(currentThread.current);
        setPendingRequest(false);
      }
      setError(errorText(error));
    } finally {
      setBusy(false);
    }
  }
  async function stop() {
    if (!currentRun) return;
    try {
      await api(`/api/v1/runs/${currentRun.id}/cancel`, "POST", {});
      await refreshThread(threadID);
    } catch (error) {
      setError(errorText(error));
    }
  }
  async function saveFeedback(value: string) {
    if (!artifact) return;
    try {
      const updated = await api<Artifact>(
        `/api/v1/artifacts/${artifact.id}/versions/${artifact.version}`,
        "PATCH",
        { feedback: value },
        { "If-Match": String(artifact.revision) },
      );
      setArtifact(updated);
      await refreshThread(threadID);
    } catch (error) {
      setError(errorText(error));
    }
  }
  const selectMember = (id: string) =>
    setSelected((current) =>
      current.includes(id)
        ? current.filter((item) => item !== id)
        : [...current, id],
    );
  const submit = (event: FormEvent) => {
    event.preventDefault();
    void send();
  };
  const updateDraft = (value: string) =>
    setDrafts((current) => ({ ...current, [draftKey]: value }));

  if (!ready)
    return (
      <main className={cx("bootstrap")}>
        <div className={cx("brand-mark")}>◈</div>
        <h1>Cocogem</h1>
        <p>让不同的思考，在同一个空间相遇。</p>
        {error ? (
          <p role="alert" className={cx("error")}>
            {error}
            <br />
            请打开终端中包含启动令牌的本地链接。
          </p>
        ) : (
          <p>正在连接本地工作空间…</p>
        )}
      </main>
    );
  return (
    <div className={cx("app-shell")}>
      <aside className={cx(`sidebar ${navOpen ? "mobile-open" : ""}`)}>
        <button
          className={cx("mobile-panel")}
          aria-label="关闭导航"
          onClick={() => setNavOpen(false)}
        >
          关闭导航
        </button>
        <a
          className={cx("brand")}
          href="/"
          onClick={(event) => {
            event.preventDefault();
            navigate(workspaceID);
          }}
        >
          <span className={cx("brand-mark")}>◈</span>
          <strong>Cocogem</strong>
          <span className={cx("version")}>v0.1</span>
        </a>
        <label className={cx("sr-only")} htmlFor="workspace-switch">
          切换工作空间
        </label>
        <select
          id="workspace-switch"
          value={workspaceID}
          onChange={(event) => navigate(event.target.value)}
        >
          <option value="">选择工作空间</option>
          {workspaces.map((ws) => (
            <option key={ws.id} value={ws.id}>
              {ws.name}
            </option>
          ))}
        </select>
        <button className={cx("new-thread")} onClick={() => setModal("thread")}>
          ＋ 新建会话
        </button>
        <label className={cx("search")}>
          <span>⌕</span>
          <input
            aria-label="搜索会话"
            placeholder="搜索会话标题"
            value={filter}
            onChange={(event) => setFilter(event.target.value)}
          />
        </label>
        <div className={cx("eyebrow")}>
          最近的思考 <span>{threads.length}</span>
        </div>
        <nav className={cx("thread-list")} aria-label="会话列表">
          {threads
            .filter((thread) => thread.title.includes(filter))
            .map((thread) => (
              <button
                key={thread.id}
                aria-label={thread.title}
                className={cx(
                  thread.id === threadID ? "thread selected" : "thread",
                )}
                onClick={() => navigate(workspaceID, thread.id)}
              >
                <span>◌</span>
                <span>{thread.title}</span>
              </button>
            ))}
          {!threads.length && (
            <p className={cx("muted empty-small")}>从一个问题开始。</p>
          )}
        </nav>
        <div className={cx("sidebar-footer")}>
          <button onClick={() => setModal("workspace")}>＋ 创建工作空间</button>
          <button
            disabled={!workspace}
            onClick={() => setModal("edit-workspace")}
          >
            编辑工作空间
          </button>
          <button onClick={() => setModal("settings")}>⚙ 服务与设置</button>
          <p>{system?.demo ? "● 离线演示 · 数据留在本机" : "● 本地工作空间"}</p>
        </div>
      </aside>
      <section className={cx("workspace-main")}>
        <header className={cx("topbar")}>
          <button
            className={cx("mobile-panel")}
            onClick={() => setNavOpen(true)}
          >
            导航
          </button>
          <div>
            <span className={cx("eyebrow")}>WORKSPACE</span>
            <strong>{workspace?.name ?? "欢迎来到 Cocogem"}</strong>
          </div>
          <div className={cx("top-actions")}>
            {threadID && (
              <button onClick={() => setModal("edit-thread")}>会话设置</button>
            )}
            {system?.demo && <span className={cx("demo-badge")}>离线演示</span>}
            <button onClick={() => setModal("agents")} disabled={!workspace}>
              成员 <span>{agents.length}</span>
            </button>
            <button
              className={cx("mobile-panel")}
              onClick={() =>
                setTab(tab === "members" ? "artifacts" : "members")
              }
            >
              资料面板
            </button>
          </div>
        </header>
        {error && (
          <div role="alert" className={cx("error-banner")}>
            {error}
            <button aria-label="关闭错误提示" onClick={() => setError("")}>
              ×
            </button>
          </div>
        )}
        <div className={cx("workspace-columns")}>
          <main className={cx("conversation")}>
            <div className={cx("conversation-heading")}>
              <div>
                <span className={cx("eyebrow")}>
                  {threadID
                    ? "SHARED CONVERSATION"
                    : "A PLACE TO THINK TOGETHER"}
                </span>
                <h1>{view?.thread.title ?? "把问题交给一个团队。"}</h1>
              </div>
              {currentRun && (
                <span
                  data-testid="run-status"
                  className={cx(`status ${currentRun.status}`)}
                >
                  {statusText[currentRun.status]}
                </span>
              )}
            </div>
            <div
              className={cx("timeline")}
              ref={timeline}
              onScroll={(event) => {
                const el = event.currentTarget;
                follows.current =
                  el.scrollHeight - el.scrollTop - el.clientHeight < 80;
                if (follows.current) setNewMessages(false);
              }}
            >
              {!threadID && (
                <div className={cx("welcome")}>
                  <p className={cx("intro")}>
                    研究一个主题，比较不同观点，或读懂一份代码。
                    <br />
                    让每位成员各展所长，让每个结论有据可查。
                  </p>
                  <div className={cx("scenario-grid")}>
                    {(
                      [
                        [
                          "discussion",
                          "✦",
                          "一起讨论",
                          "让不同观点独立表达，再梳理共识与分歧。",
                          "比较自己实现 Agent Loop 和使用 Agent 框架的取舍。",
                        ],
                        [
                          "research",
                          "⌕",
                          "做一次 Research",
                          "查找资料、核对来源，形成可追溯的报告。",
                          "研究多 Agent 协作的适用场景，说明证据和限制。",
                        ],
                        [
                          "code_analysis",
                          "⌘",
                          "理解一份代码",
                          "只读分析与独立复核，保留文件证据。",
                          "分析示例仓库如何区分成员身份与任务职责。",
                        ],
                      ] as const
                    ).map(([key, icon, title, description, sample]) => (
                      <button
                        key={key}
                        className={cx("scenario")}
                        onClick={() => {
                          setWorkflow(key);
                          updateDraft(sample);
                        }}
                      >
                        <span>{icon}</span>
                        <strong>{title}</strong>
                        <p>{description}</p>
                        <small>开始探索 ↗</small>
                      </button>
                    ))}
                  </div>
                  <div className={cx("welcome-note")}>
                    {workspace ? (
                      `${available.length} 位成员已就位。可以直接说“布偶猫帮我研究……”`
                    ) : (
                      <button
                        className={cx("primary")}
                        onClick={() => setModal("workspace")}
                      >
                        创建第一个工作空间
                      </button>
                    )}
                  </div>
                </div>
              )}
              {view?.messages.items.map((message) => (
                <MessageCard
                  key={message.id}
                  message={message}
                  agents={agents}
                />
              ))}
              {steps.length > 0 && (
                <>
                  <button onClick={() => setModal("snapshot")}>
                    查看本次配置快照
                  </button>
                  <details className={cx("execution-summary")}>
                    <summary>
                      本次协作 · {steps.length} 个步骤{" "}
                      {connection && ` · ${connection}`}
                    </summary>
                    {steps.map((step) => (
                      <div key={step.id}>
                        <strong>{step.agent_name}</strong>
                        <span>{step.role}</span>
                        <span>{statusText[step.status]}</span>
                        {step.error && <p>{step.error}</p>}
                      </div>
                    ))}
                  </details>
                </>
              )}
              {currentRun?.error && (
                <p className={cx("error")} role="status">
                  {currentRun.error}
                </p>
              )}
              {currentRun && terminal(currentRun.status) && (
                <button
                  className={cx("retry")}
                  onClick={() =>
                    void send({
                      ...currentRun.input,
                      retry_of_run_id: currentRun.id,
                    })
                  }
                  disabled={busy}
                >
                  重新运行本次协作
                </button>
              )}
            </div>
            {newMessages && (
              <button
                className={cx("new-messages")}
                onClick={() => {
                  follows.current = true;
                  if (timeline.current)
                    timeline.current.scrollTop = timeline.current.scrollHeight;
                  setNewMessages(false);
                }}
              >
                ↓ 有新消息
              </button>
            )}
            <form className={cx("composer")} onSubmit={submit}>
              {pendingRequest && (
                <p role="status">
                  上次提交等待确认。
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() =>
                      void send(pendingSubmission.current.get(threadID)?.input)
                    }
                  >
                    重试原请求
                  </button>
                </p>
              )}
              <div className={cx("composer-toolbar")}>
                <label>
                  工作方式{" "}
                  <select
                    aria-label="工作方式"
                    value={workflow}
                    onChange={(event) => {
                      setWorkflow(event.target.value as keyof typeof workflows);
                      setRouting(undefined);
                    }}
                  >
                    {Object.entries(workflows).map(([key, name]) => (
                      <option key={key} value={key}>
                        {name}
                      </option>
                    ))}
                  </select>
                </label>
                <span className={cx("muted")}>
                  {active
                    ? "当前协作结束后可继续发送"
                    : "自然语言指派 · 支持 @成员"}
                </span>
              </div>
              {(workflow === "free" || workflow === "discussion") && (
                <div className={cx("recipient-list")}>
                  {available.map((agent, index) => (
                    <button
                      type="button"
                      key={agent.id}
                      aria-pressed={selected.includes(agent.id)}
                      className={cx(
                        selected.includes(agent.id)
                          ? "recipient active"
                          : "recipient",
                      )}
                      onClick={() => selectMember(agent.id)}
                    >
                      <span>{catIcon(index)}</span>
                      {memberLabel(agent)}
                    </button>
                  ))}
                  <small>
                    {selected.length
                      ? "已指定收件人"
                      : workflow === "free"
                        ? "自动解析指派；普通讨论交给协调者"
                        : "默认所有会话成员独立讨论"}
                  </small>
                </div>
              )}
              {workflow !== "free" && (
                <div className={cx("role-selectors")}>
                  {(workflow === "discussion"
                    ? ["coordinator"]
                    : workflow === "research"
                      ? ["researcher", "critic", "coordinator"]
                      : ["analyst", "reviewer"]
                  ).map((role) => (
                    <label key={role}>
                      {
                        {
                          researcher: "研究",
                          critic: "质疑",
                          coordinator: "汇总",
                          analyst: "分析",
                          reviewer: "复核",
                        }[role]
                      }
                      <select
                        value={assignments[role] ?? ""}
                        onChange={(event) =>
                          setAssignmentChoices((current) => ({
                            ...current,
                            [draftKey]: {
                              ...current[draftKey],
                              [role]: event.target.value,
                            },
                          }))
                        }
                      >
                        <option value="">请选择成员</option>
                        {available.map((agent) => (
                          <option value={agent.id} key={agent.id}>
                            {memberLabel(agent)}
                          </option>
                        ))}
                      </select>
                    </label>
                  ))}
                </div>
              )}
              {artifactRef && (
                <div className={cx("reference-pill")}>
                  引用：{artifactRef.title} v{artifactRef.version}
                  <button
                    type="button"
                    aria-label="移除产物引用"
                    onClick={() => setArtifactRef(undefined)}
                  >
                    ×
                  </button>
                </div>
              )}
              <textarea
                aria-label="输入问题"
                placeholder="说说你想完成什么，例如：布偶猫查资料，缅因猫复核，橘猫汇总。"
                value={draft}
                onChange={(event) => updateDraft(event.target.value)}
                onKeyDown={(event) => {
                  if (
                    (event.ctrlKey || event.metaKey) &&
                    event.key === "Enter"
                  ) {
                    event.preventDefault();
                    void send();
                  }
                }}
                rows={3}
              />
              {draft.endsWith("@") && (
                <div className={cx("mention-options")}>
                  {available.map((agent) => (
                    <button
                      type="button"
                      key={agent.id}
                      onClick={() => updateDraft(draft + agent.handle + " ")}
                    >
                      {memberLabel(agent)}
                    </button>
                  ))}
                </div>
              )}
              <details className={cx("source-input")}>
                <summary>＋ 添加资料链接与研究范围</summary>
                <label>
                  公共 URL（每行一个）
                  <textarea
                    aria-label="资料链接"
                    rows={2}
                    value={urls}
                    onChange={(event) => setURLs(event.target.value)}
                    placeholder="https://…"
                  />
                </label>
                <label>
                  研究范围
                  <select
                    value={scope}
                    onChange={(event) => setScope(event.target.value)}
                  >
                    <option value="given_sources">研究给定资料</option>
                    <option value="web_discovery">在线发现资料</option>
                  </select>
                </label>
              </details>
              {routing && (
                <p className={cx("routing-preview")} role="status">
                  {routing.clarification ||
                    `${routing.explanation} · ${
                      routing.targets
                        ?.map((id) => {
                          const agent = agents.find((a) => a.id === id);
                          return agent ? memberLabel(agent) : "";
                        })
                        .filter(Boolean)
                        .join("、") ||
                      workflows[routing.workflow as keyof typeof workflows]
                    }`}
                </p>
              )}
              <div className={cx("composer-bottom")}>
                <small>
                  ⌘ / Ctrl + Enter 发送 ·{" "}
                  {system?.demo
                    ? "当前为确定性离线示例"
                    : "问题与读取片段会发送至配置的模型"}
                </small>
                {active ? (
                  <button
                    type="button"
                    className={cx("stop")}
                    onClick={() => void stop()}
                    disabled={currentRun?.status === "canceling"}
                  >
                    ■{" "}
                    {currentRun?.status === "canceling"
                      ? "正在停止"
                      : "停止协作"}
                  </button>
                ) : (
                  <button
                    className={cx("primary")}
                    type="submit"
                    disabled={busy || !workspace || !draft.trim()}
                  >
                    {busy ? "正在解析与提交…" : "发送 ↑"}
                  </button>
                )}
              </div>
            </form>
          </main>
          <aside className={cx("context-panel")}>
            <div
              className={cx("panel-tabs")}
              role="tablist"
              aria-label="协作资料"
            >
              {(
                [
                  ["members", "成员"],
                  ["sources", "来源"],
                  ["artifacts", "产物"],
                  ["tools", "工具"],
                ] as const
              ).map(([key, name]) => (
                <button
                  key={key}
                  role="tab"
                  id={`tab-${key}`}
                  aria-controls="context-panel"
                  tabIndex={tab === key ? 0 : -1}
                  onKeyDown={(event) => {
                    const keys = [
                      "members",
                      "sources",
                      "artifacts",
                      "tools",
                    ] as const;
                    let index = keys.indexOf(tab);
                    if (event.key === "ArrowRight") index = (index + 1) % 4;
                    else if (event.key === "ArrowLeft") index = (index + 3) % 4;
                    else if (event.key === "Home") index = 0;
                    else if (event.key === "End") index = 3;
                    else return;
                    event.preventDefault();
                    setTab(keys[index]);
                    document.getElementById(`tab-${keys[index]}`)?.focus();
                  }}
                  aria-selected={tab === key}
                  onClick={() => setTab(key)}
                >
                  {name}
                </button>
              ))}
            </div>
            <div
              className={cx("panel-body")}
              id="context-panel"
              role="tabpanel"
              aria-labelledby={`tab-${tab}`}
            >
              {tab === "members" && (
                <>
                  <div className={cx("eyebrow")}>THE TEAM</div>
                  <h2>各有所长，一起思考。</h2>
                  {agents.map((agent, index) => (
                    <article className={cx("member-card")} key={agent.id}>
                      <div className={cx(`avatar cat-${index % 3}`)}>
                        {catIcon(index)}
                      </div>
                      <div>
                        <strong>{agent.display_name}</strong>
                        <small>
                          @{agent.handle} {agent.disabled && "· 已停用"}
                        </small>
                        <p>{agent.role_description}</p>
                        <span className={cx("model-label")}>
                          {agent.executor ? `${agent.executor} · ` : ""}
                          {agent.model === "default"
                            ? "CLI 默认模型"
                            : agent.model}
                        </span>
                      </div>
                    </article>
                  ))}
                  <button
                    onClick={() => setModal("agents")}
                    disabled={!workspace}
                  >
                    管理成员与称呼
                  </button>
                  <p className={cx("panel-note")}>
                    名字、职责和模型各自独立。共享结论，保留不同意见。
                  </p>
                </>
              )}
              {tab === "sources" && (
                <>
                  <h2>每个结论，都能回到来源。</h2>
                  {!view?.sources.length && (
                    <p className={cx("muted")}>
                      成员读取资料后，来源会出现在这里。
                    </p>
                  )}
                  {view?.sources.map((item) => (
                    <button
                      className={cx("source-card")}
                      key={item.id}
                      onClick={() => setSource(item)}
                    >
                      <span>
                        {item.kind === "file"
                          ? "FILE"
                          : item.kind === "search_snippet"
                            ? "搜索摘要 · 未读原文"
                            : "WEB · 已捕获原文"}
                      </span>
                      <strong>{item.title}</strong>
                      <small>{item.path || item.url}</small>
                    </button>
                  ))}
                  {source && (
                    <section
                      data-testid="source-detail"
                      className={cx("source-detail")}
                    >
                      <h3>{source.title}</h3>
                      <small>
                        {new Date(source.captured_at).toLocaleString()}{" "}
                        {source.path &&
                          `L${source.start_line}–${source.end_line}`}
                      </small>
                      <pre>{source.content}</pre>
                      {source.truncated && <p>内容已截断。</p>}
                      <small>
                        {source.sha256 && `SHA-256: ${source.sha256}`}
                      </small>
                    </section>
                  )}
                </>
              )}
              {tab === "artifacts" && (
                <>
                  <h2>把思考留下来。</h2>
                  {!artifacts.length && (
                    <p className={cx("muted")}>
                      协作生成的笔记和报告会出现在这里。
                    </p>
                  )}
                  {artifacts.map((item) => (
                    <button
                      className={cx("artifact-card")}
                      key={`${item.id}:${item.version}`}
                      onClick={() => setArtifact(item)}
                    >
                      <span>
                        {item.type} · v{item.version}
                      </span>
                      <strong>{item.title}</strong>
                      <small>
                        {item.feedback === "accepted"
                          ? "已接受"
                          : item.feedback === "dismissed"
                            ? "已搁置"
                            : "待人工查看"}
                      </small>
                    </button>
                  ))}
                  {artifact && (
                    <section
                      data-testid="artifact-detail"
                      className={cx("artifact-detail")}
                    >
                      <h3>
                        {artifact.title} <small>v{artifact.version}</small>
                      </h3>
                      {artifact.demo && (
                        <p className={cx("demo-badge")}>离线演示产物</p>
                      )}
                      <Markdown text={artifact.body} />
                      {(artifact.findings ?? []).map((finding, index) => (
                        <article key={index}>
                          <h4>{finding.title}</h4>
                          <p>
                            {finding.status} {finding.severity ?? ""}
                          </p>
                          <Markdown text={finding.detail} />
                          {(finding.source_ids ?? []).map((id) => (
                            <button
                              key={id}
                              onClick={() =>
                                void api<Source>(`/api/v1/sources/${id}`)
                                  .then((value) => {
                                    setSource(value);
                                    setTab("sources");
                                  })
                                  .catch((error) => setError(errorText(error)))
                              }
                            >
                              查看发现来源 {id.slice(0, 8)}
                            </button>
                          ))}
                        </article>
                      ))}
                      {(artifact.limitations ?? []).map((limit, index) => (
                        <p key={index}>限制：{limit}</p>
                      ))}
                      <h4>捕获来源</h4>
                      {(artifact.source_ids ?? []).map((id) => (
                        <button
                          key={id}
                          onClick={() =>
                            void api<Source>(`/api/v1/sources/${id}`)
                              .then((value) => {
                                setSource(value);
                                setTab("sources");
                              })
                              .catch((error) => setError(errorText(error)))
                          }
                        >
                          打开来源 {id.slice(0, 8)}
                        </button>
                      ))}
                      <div className={cx("artifact-actions")}>
                        <button
                          onClick={() =>
                            void navigator.clipboard
                              .writeText(artifact.body)
                              .catch((error) => setError(errorText(error)))
                          }
                        >
                          复制
                        </button>
                        <a
                          className={cx("button")}
                          href={`/api/v1/artifacts/${artifact.id}/versions/${artifact.version}/export`}
                        >
                          下载 Markdown
                        </a>
                        <button onClick={() => setArtifactRef(artifact)}>
                          引用后追问
                        </button>
                        <button onClick={() => void saveFeedback("accepted")}>
                          接受
                        </button>
                        <button onClick={() => void saveFeedback("dismissed")}>
                          搁置
                        </button>
                      </div>
                      <p className={cx("muted")}>
                        人工标记不改变成员原文或任务结果。
                      </p>
                    </section>
                  )}
                </>
              )}
              {tab === "tools" && (
                <>
                  <h2>执行过程，清楚可见。</h2>
                  <label>
                    历史协作
                    <select
                      value={inspectedRun?.id ?? ""}
                      onChange={(event) => {
                        setEvents([]);
                        setInspectedRunID(event.target.value);
                      }}
                    >
                      {view?.runs.map((run, index) => (
                        <option key={run.id} value={run.id}>
                          第 {index + 1} 次 · {statusText[run.status]}
                        </option>
                      ))}
                    </select>
                  </label>
                  {!events.length && (
                    <p className={cx("muted")}>
                      工具与交接记录在本次协作期间实时出现。
                    </p>
                  )}
                  <ToolTimeline
                    events={events}
                    agents={inspectedRun?.agents ?? []}
                    sources={view?.sources ?? []}
                    onSource={(id) =>
                      void api<Source>(`/api/v1/sources/${id}`)
                        .then((value) => {
                          setSource(value);
                          setTab("sources");
                        })
                        .catch((error) => setError(errorText(error)))
                    }
                  />
                </>
              )}
            </div>
          </aside>
        </div>
      </section>
      {(modal === "workspace" || modal === "edit-workspace") && system && (
        <WorkspaceForm
          current={modal === "edit-workspace" ? workspace : undefined}
          system={system}
          close={() => setModal(null)}
          save={async (value) => {
            const saved = await api<Workspace>(
              value.id
                ? `/api/v1/workspaces/${value.id}`
                : "/api/v1/workspaces",
              value.id ? "PATCH" : "POST",
              {
                name: value.name,
                description: value.description,
                root: value.root,
                consent_version: value.consent_version,
              },
              value.id ? { "If-Match": String(value.revision) } : {},
            );
            await loadSpaces();
            setModal(null);
            navigate(saved.id);
          }}
        />
      )}
      {modal === "agents" && system && (
        <MembersForm
          agents={agents}
          system={system}
          workspaceID={workspaceID}
          close={() => setModal(null)}
          save={async (value) => {
            const saved = await api<Agent>(
              value.id
                ? `/api/v1/agents/${value.id}`
                : `/api/v1/workspaces/${workspaceID}/agents`,
              value.id ? "PATCH" : "POST",
              value,
              value.id ? { "If-Match": String(value.revision) } : {},
            );
            await loadMembers(workspaceID);
            return saved;
          }}
        />
      )}
      {(modal === "thread" || modal === "edit-thread") && workspace && (
        <ThreadForm
          current={modal === "edit-thread" ? view?.thread : undefined}
          agents={available}
          close={() => setModal(null)}
          save={async (value) => {
            const th = await api<Thread>(
              value.id
                ? `/api/v1/threads/${value.id}`
                : `/api/v1/workspaces/${workspaceID}/threads`,
              value.id ? "PATCH" : "POST",
              value,
              value.id ? { "If-Match": String(value.revision) } : {},
            );
            setWorkflowChoices((current) => ({
              ...current,
              [th.id]: th.workflow as keyof typeof workflows,
            }));
            setAssignmentChoices((current) => ({ ...current, [th.id]: {} }));
            setModal(null);
            await loadThreads(workspaceID);
            navigate(workspaceID, th.id);
          }}
        />
      )}
      {modal === "snapshot" && currentRun && (
        <Modal title="本次协作配置快照" close={() => setModal(null)}>
          <p>
            流程：{currentRun.input.workflow} · 创建时间：
            {currentRun.created_at}
          </p>
          {currentRun.agents?.map((agent) => (
            <section key={agent.id}>
              <h3>
                {agent.display_name} (@{agent.handle})
              </h3>
              <p>
                配置版本 {agent.revision} ·{" "}
                {agent.executor ? `${agent.executor} · ` : ""}模型{" "}
                {agent.model === "default" ? "CLI 默认" : agent.model}
              </p>
              <p>{agent.role_description}</p>
              <p>工具：{agent.tools?.join(", ")}</p>
            </section>
          ))}
          <p>历史快照不随成员改名、模型或权限编辑而改变。</p>
        </Modal>
      )}
      {modal === "settings" && system && (
        <Modal title="服务与设置" close={() => setModal(null)}>
          <p>
            {system.demo
              ? "当前使用确定性离线模型，仅访问随包资料。"
              : "成员调用本机 Claude、Codex 或 Gemini CLI，复用本机的模型、供应商与认证配置。网页不会读取凭证。"}
          </p>
          <dl>
            <dt>执行限制</dt>
            <dd>
              {Object.entries(system.limits ?? {})
                .map(([key, value]) => `${key}: ${value}`)
                .join(" · ")}
            </dd>
            <dt>{system.demo ? "模型" : "本机 CLI"}</dt>
            <dd>
              {system.demo
                ? `${statusText[system.model_status]} · ${system.models.join(", ")}`
                : system.executors?.map((executor) => (
                    <p key={executor.id}>
                      {executor.id} ·{" "}
                      {executor.installed
                        ? `已安装 · ${executor.version}`
                        : "未检测到"}
                      {executor.reason
                        ? ` · ${executor.supported === false ? "不支持" : "使用限制"}：${executor.reason}`
                        : ""}
                    </p>
                  ))}
            </dd>
            <dt>在线搜索</dt>
            <dd>{statusText[system.search_status]}</dd>
            <dt>允许的资料目录</dt>
            <dd>{system.roots.join("\n") || "未设置"}</dd>
          </dl>
          <p>
            真实运行会通过 CLI
            发送问题与读取片段至模型服务；搜索会发送查询词。已安装不代表连接或模型可用。
          </p>
          {!system.demo && (
            <>
              <label>
                验证 CLI
                <select
                  value={verifyExecutor}
                  onChange={(event) => {
                    setVerifyExecutor(event.target.value);
                    setVerificationError("");
                  }}
                >
                  {["claude", "codex", "gemini"].map((id) => (
                    <option key={id} value={id}>
                      {id}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                验证模型（留空使用 CLI 默认）
                <input
                  value={verifyModel}
                  onChange={(event) => {
                    setVerifyModel(event.target.value);
                    setVerificationError("");
                  }}
                  placeholder="CLI 默认模型"
                />
              </label>
            </>
          )}
          <button
            disabled={
              verifying ||
              (!system.demo &&
                !system.executors?.some(
                  (executor) =>
                    executor.id === verifyExecutor &&
                    executor.installed &&
                    executor.supported !== false,
                ))
            }
            onClick={() => {
              setVerifying(true);
              setVerificationError("");
              void api(
                "/api/v1/system/verify",
                "POST",
                system.demo
                  ? {}
                  : {
                      executor: verifyExecutor,
                      model: verifyModel.trim() || "default",
                    },
              )
                .then(() => api<System>("/api/v1/system"))
                .then(setSystem)
                .catch((error) => setVerificationError(errorText(error)))
                .finally(() => setVerifying(false));
            }}
          >
            {verifying ? "正在验证…" : "验证模型连接（可能产生用量）"}
          </button>
          {verificationError && (
            <p role="alert" className={cx("error")}>
              {verificationError}
            </p>
          )}
          {system.verified[
            system.demo
              ? "model"
              : `${verifyExecutor}:${verifyModel.trim() || "default"}`
          ] && (
            <p>
              验证时间：
              {new Date(
                system.verified[
                  system.demo
                    ? "model"
                    : `${verifyExecutor}:${verifyModel.trim() || "default"}`
                ],
              ).toLocaleString()}{" "}
              · {system.demo ? "离线演示检查通过" : "该 CLI 与模型上次已验证"}
            </p>
          )}
        </Modal>
      )}
    </div>
  );
}
