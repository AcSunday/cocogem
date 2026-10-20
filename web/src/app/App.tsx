import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  APIError,
  setCSRF,
  terminal,
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
import { ArtifactReader } from "../components/ArtifactReader";
import { Composer, type WorkflowKey } from "../components/Composer";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { ContextPanel, type PanelTab } from "../components/ContextPanel";
import { Icon } from "../components/Icon";
import { MessageCard } from "../components/MessageCard";
import { RunDivider } from "../components/RunDivider";
import { SettingsDialog, SnapshotDialog } from "../components/SettingsDialog";
import { Sidebar } from "../components/Sidebar";
import { MembersForm } from "../components/forms/MembersForm";
import { ThreadForm } from "../components/forms/ThreadForm";
import { WorkspaceForm } from "../components/forms/WorkspaceForm";
import {
  defaultAssignments,
  errorText,
  threadTitle,
} from "../lib/presentation";
import { cx } from "../styles/classes";

const pathSelection = () => {
  const match = location.pathname.match(/^\/w\/([^/]+)(?:\/t\/([^/]+))?/);
  return { workspace: match?.[1] ?? "", thread: match?.[2] ?? "" };
};

const scenarios = [
  {
    key: "discussion",
    icon: "discussion",
    title: "一起讨论",
    description: "让不同观点独立表达，再梳理共识与分歧。",
    sample: "比较自己实现 Agent Loop 和使用 Agent 框架的取舍。",
  },
  {
    key: "research",
    icon: "research",
    title: "做一次 Research",
    description: "查找资料、核对来源，形成可追溯的报告。",
    sample: "研究多 Agent 协作的适用场景，说明证据和限制。",
  },
  {
    key: "code_analysis",
    icon: "code",
    title: "理解一份代码",
    description: "只读分析与独立复核，保留文件证据。",
    sample: "分析示例仓库如何区分成员身份与任务职责。",
  },
] as const;

type ModalState =
  | { kind: "workspace" | "edit-workspace" | "agents" | "settings" }
  | { kind: "thread" | "edit-thread" }
  | { kind: "snapshot"; run: Run }
  | { kind: "artifact" }
  | { kind: "delete-thread"; thread: Thread }
  | { kind: "delete-workspace"; workspace: Workspace };

export function App() {
  const [system, setSystem] = useState<System>();
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);
  const [workspaceID, setWorkspaceID] = useState(pathSelection().workspace);
  const [threadID, setThreadID] = useState(pathSelection().thread);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [threads, setThreads] = useState<Thread[]>([]);
  const [view, setView] = useState<ThreadView>();
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [stepsByRun, setStepsByRun] = useState<Record<string, Step[]>>({});
  const [events, setEvents] = useState<Event[]>([]);
  const [error, setError] = useState("");
  const [ready, setReady] = useState(false);
  const [busy, setBusy] = useState(false);
  const [modal, setModal] = useState<ModalState | null>(null);
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [workflowChoices, setWorkflowChoices] = useState<
    Record<string, WorkflowKey>
  >({});
  const [selected, setSelected] = useState<string[]>([]);
  const [assignmentChoices, setAssignmentChoices] = useState<
    Record<string, Record<string, string>>
  >({});
  const [urls, setURLs] = useState("");
  const [scope, setScope] = useState("given_sources");
  const [routing, setRouting] = useState<Routing>();
  const [tab, setTab] = useState<PanelTab>("members");
  const [source, setSource] = useState<Source>();
  const [artifact, setArtifact] = useState<Artifact>();
  const [artifactRef, setArtifactRef] = useState<Artifact>();
  const [connection, setConnection] = useState("");
  const [newMessages, setNewMessages] = useState(false);
  const [pendingRequest, setPendingRequest] = useState(false);
  const [navOpen, setNavOpen] = useState(false);
  const [panelOpen, setPanelOpen] = useState(false);
  const [inspectedRunID, setInspectedRunID] = useState("");
  const timeline = useRef<HTMLDivElement>(null);
  const follows = useRef(true);
  const currentThread = useRef(threadID);
  const currentWorkspace = useRef(workspaceID);
  currentWorkspace.current = workspaceID;
  currentThread.current = threadID;
  const refreshGeneration = useRef(0);
  // Steps of finished runs never change, so they are fetched once per run.
  const settledSteps = useRef(new Set<string>());
  const pendingSubmission = useRef(
    new Map<string, { input: RunInput; key: string; threadID: string }>(),
  );
  const workspace = workspaces.find((item) => item.id === workspaceID);
  const runs = view?.runs ?? [];
  const currentRun = runs.at(-1);
  const inspectedRun =
    runs.find((run) => run.id === inspectedRunID) ?? currentRun;
  const active = runs.some((run) => !terminal(run.status));
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
    (view?.thread.workflow as WorkflowKey | undefined) ??
    "free";
  const setWorkflow = (value: WorkflowKey) => {
    setWorkflowChoices((current) => ({ ...current, [draftKey]: value }));
    setRouting(undefined);
  };
  const assignments: Record<string, string> = {
    ...defaultAssignments(threadMembers, view?.thread.coordinator_id),
    ...Object.fromEntries(
      Object.entries(assignmentChoices[draftKey] ?? {}).filter(([, id]) =>
        available.some((agent) => agent.id === id),
      ),
    ),
  };
  const updateDraft = (value: string) =>
    setDrafts((current) => ({ ...current, [draftKey]: value }));

  const navigate = useCallback((ws: string, th = "", push = true) => {
    setPendingRequest(pendingSubmission.current.has(th));
    setNavOpen(false);
    setPanelOpen(false);
    currentThread.current = th;
    currentWorkspace.current = ws;
    refreshGeneration.current++;
    setWorkspaceID(ws);
    setThreadID(th);
    setView(undefined);
    setEvents([]);
    setInspectedRunID("");
    setStepsByRun({});
    settledSteps.current.clear();
    setArtifacts([]);
    setSource(undefined);
    setArtifactRef(undefined);
    setRouting(undefined);
    setSelected([]);
    if (push)
      history.pushState({}, "", ws ? `/w/${ws}${th ? `/t/${th}` : ""}` : "/");
  }, []);
  const loadSpaces = useCallback(async () => {
    const result = await api<{ items: Workspace[] }>("/api/v1/workspaces");
    setWorkspaces(result.items ?? []);
    return result.items ?? [];
  }, []);
  const loadMembers = useCallback(async (id: string) => {
    const result = await api<{ items: Agent[] }>(
      `/api/v1/workspaces/${id}/agents`,
    );
    if (currentWorkspace.current === id) setAgents(result.items ?? []);
  }, []);
  const loadThreads = useCallback(async (id: string) => {
    const items: Thread[] = [];
    let cursor = "";
    do {
      const result = await api<{ items: Thread[]; next_cursor: string }>(
        `/api/v1/workspaces/${id}/threads?limit=100&cursor=${cursor}`,
      );
      items.push(...(result.items ?? []));
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
    setArtifacts(deliverables.items ?? []);
    const stale = result.runs.filter(
      (run) => !settledSteps.current.has(run.id),
    );
    const loaded = await Promise.all(
      stale.map((run) =>
        api<{ run: Run; steps: Step[] }>(`/api/v1/runs/${run.id}`),
      ),
    );
    if (currentThread.current !== id) return;
    for (const item of loaded)
      if (terminal(item.run.status)) settledSteps.current.add(item.run.id);
    setStepsByRun((current) => ({
      ...current,
      ...Object.fromEntries(
        loaded.map((item) => [item.run.id, item.steps ?? []]),
      ),
    }));
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
    if (!navOpen && !panelOpen) return;
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !document.querySelector("dialog[open]")) {
        setNavOpen(false);
        setPanelOpen(false);
      }
    };
    addEventListener("keydown", escape);
    return () => removeEventListener("keydown", escape);
  }, [navOpen, panelOpen]);
  useEffect(() => {
    if (ready && workspaceID) {
      void Promise.all([
        loadMembers(workspaceID),
        loadThreads(workspaceID),
      ]).catch((error) => setError(errorText(error)));
    } else if (ready) {
      setAgents([]);
      setThreads([]);
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
  }, [view?.messages.items, stepsByRun]);

  async function createThread(text: string) {
    if (!workspaceID || available.length < 1)
      throw new Error("请先创建工作空间和可用成员");
    const thread = await api<Thread>(
      `/api/v1/workspaces/${workspaceID}/threads`,
      "POST",
      {
        title: threadTitle(text),
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
      [thread.id]: current[`new:${workspaceID}`] ?? text,
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
  const openSource = (value: Source | string) => {
    setTab("sources");
    setPanelOpen(true);
    if (typeof value !== "string") return setSource(value);
    void api<Source>(`/api/v1/sources/${value}`)
      .then(setSource)
      .catch((error) => setError(errorText(error)));
  };
  const close = () => setModal(null);

  if (!ready)
    return (
      <main className={cx("bootstrap")}>
        <span className={cx("brand-mark")}>
          <Icon name="logo" size={22} />
        </span>
        <h1>Cocogem</h1>
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

  const messages = view?.messages.items ?? [];
  const agentIndex = (id?: string) =>
    Math.max(
      0,
      agents.findIndex((agent) => agent.id === id),
    );
  const stepRole = (runID: string, stepID: string) =>
    stepsByRun[runID]?.find((step) => step.id === stepID)?.role;
  return (
    <div className={cx("app-shell")}>
      <a className={cx("skip-link")} href="#composer-input">
        跳到输入框
      </a>
      <Sidebar
        open={navOpen}
        close={() => setNavOpen(false)}
        demo={!!system?.demo}
        workspaces={workspaces}
        workspaceID={workspaceID}
        workspace={workspace}
        threads={threads}
        threadID={threadID}
        onWorkspace={(id) => navigate(id)}
        onHome={() => navigate(workspaceID)}
        onThread={(id) => navigate(workspaceID, id)}
        onNewThread={() => setModal({ kind: "thread" })}
        onDeleteThread={(thread) => setModal({ kind: "delete-thread", thread })}
        onCreateWorkspace={() => setModal({ kind: "workspace" })}
        onEditWorkspace={() => setModal({ kind: "edit-workspace" })}
        onSettings={() => setModal({ kind: "settings" })}
      />
      <div
        className={cx(`backdrop ${navOpen || panelOpen ? "open" : ""}`)}
        aria-hidden="true"
        onClick={() => {
          setNavOpen(false);
          setPanelOpen(false);
        }}
      />
      <section className={cx("workspace-main")}>
        <header className={cx("topbar")}>
          <button
            className={cx("icon-button mobile-only")}
            aria-label="打开导航"
            aria-expanded={navOpen}
            onClick={() => setNavOpen(true)}
          >
            <Icon name="menu" size={20} />
          </button>
          <div className={cx("breadcrumb")}>
            <strong>{workspace?.name ?? "Cocogem"}</strong>
            {view && <span>/ {view.thread.title}</span>}
          </div>
          <div className={cx("top-actions")}>
            {system?.demo && <span className={cx("demo-badge")}>离线演示</span>}
            {threadID && (
              <button
                className={cx("icon-button")}
                aria-label="会话设置"
                title="会话设置"
                onClick={() => setModal({ kind: "edit-thread" })}
              >
                <Icon name="settings" size={18} />
              </button>
            )}
            <button
              className={cx("desktop-only")}
              onClick={() => setModal({ kind: "agents" })}
              disabled={!workspace}
            >
              <Icon name="users" size={16} /> 成员
              <span className={cx("count")}>{agents.length}</span>
            </button>
            <button
              className={cx("icon-button panel-toggle")}
              aria-label="打开资料面板"
              aria-expanded={panelOpen}
              onClick={() => setPanelOpen(true)}
            >
              <Icon name="panel" size={20} />
            </button>
          </div>
        </header>
        {error && (
          <div role="alert" className={cx("error-banner")}>
            <span>{error}</span>
            <button
              className={cx("icon-button small")}
              aria-label="关闭错误提示"
              onClick={() => setError("")}
            >
              <Icon name="close" size={14} />
            </button>
          </div>
        )}
        <div className={cx("workspace-columns")}>
          <main className={cx("conversation")}>
            {view && (
              <div className={cx("conversation-heading")}>
                <h1>{view.thread.title}</h1>
              </div>
            )}
            <div className={cx("timeline-wrap")}>
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
                  <Welcome
                    workspace={workspace}
                    members={available.length}
                    onCreate={() => setModal({ kind: "workspace" })}
                    onPick={(key, sample) => {
                      setWorkflow(key);
                      updateDraft(sample);
                      document.getElementById("composer-input")?.focus();
                    }}
                  />
                )}
                <div className={cx("timeline-inner")}>
                  {runs.map((run, index) => {
                    const own = messages.filter((m) => m.run_id === run.id);
                    return (
                      <div className={cx("run-segment")} key={run.id}>
                        <RunDivider
                          run={run}
                          index={index + 1}
                          steps={stepsByRun[run.id]}
                          connection={
                            run.id === inspectedRun?.id ? connection : ""
                          }
                          latest={run.id === currentRun?.id}
                          busy={busy}
                          onSnapshot={() => setModal({ kind: "snapshot", run })}
                          onRetry={() =>
                            void send({ ...run.input, retry_of_run_id: run.id })
                          }
                        />
                        {own.map((message) => (
                          <MessageCard
                            key={message.id}
                            message={message}
                            index={agentIndex(message.agent_id)}
                            role={stepRole(run.id, message.step_id)}
                          />
                        ))}
                      </div>
                    );
                  })}
                </div>
              </div>
              {newMessages && (
                <button
                  className={cx("new-messages small")}
                  onClick={() => {
                    follows.current = true;
                    if (timeline.current)
                      timeline.current.scrollTop =
                        timeline.current.scrollHeight;
                    setNewMessages(false);
                  }}
                >
                  <Icon name="down" size={14} /> 有新消息
                </button>
              )}
            </div>
            <Composer
              agents={threadID ? threadMembers : available}
              allAgents={agents}
              draft={draft}
              setDraft={updateDraft}
              workflow={workflow}
              setWorkflow={setWorkflow}
              selected={selected}
              toggleSelected={(id) =>
                setSelected((current) =>
                  current.includes(id)
                    ? current.filter((item) => item !== id)
                    : [...current, id],
                )
              }
              assignments={assignments}
              setAssignment={(role, id) =>
                setAssignmentChoices((current) => ({
                  ...current,
                  [draftKey]: { ...current[draftKey], [role]: id },
                }))
              }
              urls={urls}
              setURLs={setURLs}
              scope={scope}
              setScope={setScope}
              artifactRef={artifactRef}
              clearArtifactRef={() => setArtifactRef(undefined)}
              routing={routing}
              active={!!active}
              canceling={currentRun?.status === "canceling"}
              busy={busy}
              disabled={!workspace}
              demo={!!system?.demo}
              pending={
                pendingRequest && (
                  <p className={cx("notice")} role="status">
                    上次提交等待确认。
                    <button
                      type="button"
                      className={cx("small")}
                      disabled={busy}
                      onClick={() =>
                        void send(
                          pendingSubmission.current.get(threadID)?.input,
                        )
                      }
                    >
                      重试原请求
                    </button>
                  </p>
                )
              }
              onSend={() => void send()}
              onStop={() => void stop()}
            />
          </main>
          <ContextPanel
            open={panelOpen}
            close={() => setPanelOpen(false)}
            tab={tab}
            setTab={setTab}
            agents={agents}
            canManage={!!workspace}
            onManage={() => setModal({ kind: "agents" })}
            sources={view?.sources ?? []}
            source={source}
            onSource={openSource}
            artifacts={artifacts}
            onArtifact={(item) => {
              setArtifact(item);
              setModal({ kind: "artifact" });
            }}
            runs={runs}
            inspectedRun={inspectedRun}
            onInspect={(id) => {
              setEvents([]);
              setInspectedRunID(id);
            }}
            events={events}
          />
        </div>
      </section>
      {(modal?.kind === "workspace" || modal?.kind === "edit-workspace") &&
        system && (
          <WorkspaceForm
            current={modal.kind === "edit-workspace" ? workspace : undefined}
            system={system}
            close={close}
            onDelete={
              modal.kind === "edit-workspace" && workspace
                ? () => setModal({ kind: "delete-workspace", workspace })
                : undefined
            }
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
              close();
              navigate(saved.id, value.id ? threadID : "");
            }}
          />
        )}
      {modal?.kind === "agents" && system && (
        <MembersForm
          agents={agents}
          system={system}
          workspaceID={workspaceID}
          close={close}
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
      {(modal?.kind === "thread" || modal?.kind === "edit-thread") &&
        workspace && (
          <ThreadForm
            current={modal.kind === "edit-thread" ? view?.thread : undefined}
            agents={available}
            close={close}
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
                [th.id]: th.workflow as WorkflowKey,
              }));
              setAssignmentChoices((current) => ({ ...current, [th.id]: {} }));
              close();
              await loadThreads(workspaceID);
              navigate(workspaceID, th.id);
            }}
          />
        )}
      {modal?.kind === "snapshot" && (
        <SnapshotDialog run={modal.run} close={close} />
      )}
      {modal?.kind === "settings" && system && (
        <SettingsDialog system={system} setSystem={setSystem} close={close} />
      )}
      {modal?.kind === "artifact" && artifact && (
        <ArtifactReader
          artifact={artifact}
          sources={view?.sources ?? []}
          close={close}
          onFeedback={(value) => void saveFeedback(value)}
          onReference={() => {
            setArtifactRef(artifact);
            close();
            setPanelOpen(false);
            requestAnimationFrame(() =>
              document.getElementById("composer-input")?.focus(),
            );
          }}
          onSource={(id) => {
            close();
            openSource(id);
          }}
          onError={(error) => setError(errorText(error))}
        />
      )}
      {modal?.kind === "delete-thread" && (
        <ConfirmDialog
          title="删除会话"
          action="删除会话"
          close={close}
          confirm={async () => {
            await api(`/api/v1/threads/${modal.thread.id}`, "DELETE");
            close();
            pendingSubmission.current.delete(modal.thread.id);
            await loadThreads(workspaceID);
            if (modal.thread.id === threadID) navigate(workspaceID);
          }}
        >
          <p>
            “{modal.thread.title}
            ”的消息、协作记录、来源和产物会被永久删除，无法恢复。
          </p>
        </ConfirmDialog>
      )}
      {modal?.kind === "delete-workspace" && (
        <ConfirmDialog
          title="删除工作空间"
          action="删除工作空间"
          close={close}
          confirm={async () => {
            await api(`/api/v1/workspaces/${modal.workspace.id}`, "DELETE");
            close();
            const spaces = await loadSpaces();
            navigate(spaces[0]?.id ?? "");
          }}
        >
          <p>
            “{modal.workspace.name}”及其中的 {threads.length}{" "}
            个会话、成员、来源和产物会被永久删除，无法恢复。
          </p>
        </ConfirmDialog>
      )}
    </div>
  );
}

/** Without a workspace there is one thing to do; with one, scenarios prefill the composer. */
function Welcome({
  workspace,
  members,
  onCreate,
  onPick,
}: {
  workspace?: Workspace;
  members: number;
  onCreate: () => void;
  onPick: (key: WorkflowKey, sample: string) => void;
}) {
  if (!workspace)
    return (
      <div className={cx("welcome")}>
        <h1>把问题交给一个团队。</h1>
        <div className={cx("onboarding")}>
          <p>Cocogem 让几位成员分工研究、互相质疑，再汇总成有来源的结论。</p>
          <ol>
            <li>创建一个工作空间，会自带三位默认成员</li>
            <li>选择讨论、Research 或代码分析，提出问题</li>
            <li>查看过程、来源和报告，继续追问</li>
          </ol>
          <button className={cx("primary")} onClick={onCreate}>
            <Icon name="plus" /> 创建第一个工作空间
          </button>
        </div>
      </div>
    );
  return (
    <div className={cx("welcome")}>
      <h1>把问题交给一个团队。</h1>
      <p className={cx("intro")}>
        研究一个主题、比较不同观点，或读懂一份代码。每位成员各展所长，每个结论有据可查。
      </p>
      <div className={cx("scenario-grid")}>
        {scenarios.map((item) => (
          <button
            key={item.key}
            className={cx("scenario")}
            onClick={() => onPick(item.key, item.sample)}
          >
            <span className={cx("scenario-icon")}>
              <Icon name={item.icon} size={18} />
            </span>
            <strong>{item.title}</strong>
            <p>{item.description}</p>
          </button>
        ))}
      </div>
      <p className={cx("welcome-note")}>
        {members} 位成员已就位。也可以直接写“布偶猫帮我研究……”，或输入 @
        指定成员。
      </p>
    </div>
  );
}
