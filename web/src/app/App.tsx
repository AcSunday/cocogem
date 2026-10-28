import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  APIError,
  setCSRF,
  terminal,
  mergeEvents,
  mergeMessage,
  loadThreadPage,
  applyMessageDelta,
  eventMessage,
  eventHistory,
  EVENT_WINDOW,
  type Agent,
  type Artifact,
  type Attempt,
  type Event,
  type Message,
  type Routing,
  type Run,
  type RunInput,
  type RunView,
  type Settings,
  type Source,
  type Step,
  type System,
  type Thread,
  type ThreadView,
  type Workspace,
} from "../api/client";
import { AuthScreen } from "../components/AuthScreen";
import { ArtifactReader } from "../components/ArtifactReader";
import type { Appearance } from "../components/AppearanceSelect";
import { Avatar } from "../components/Avatar";
import { Composer, type WorkflowKey } from "../components/Composer";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { ContextPanel, type PanelTab } from "../components/ContextPanel";
import { Icon } from "../components/Icon";
import { RunSegment } from "../components/RunSegment";
import { SettingsDialog, SnapshotDialog } from "../components/SettingsDialog";
import { Sidebar } from "../components/Sidebar";
import { ThreadForm } from "../components/forms/ThreadForm";
import { ThreadRenameForm } from "../components/forms/ThreadRenameForm";
import { WorkspaceForm } from "../components/forms/WorkspaceForm";
import { WorkspaceMembersForm } from "../components/forms/WorkspaceMembersForm";
import {
  conversationCoordinator,
  errorText,
  livenessFromAttempts,
  mergeAttemptLiveness,
  nextLiveness,
  seedLiveness,
  threadTitle,
  workflows,
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
  | { kind: "rename-thread"; thread: Thread }
  | { kind: "delete-thread"; thread: Thread }
  | { kind: "delete-workspace"; workspace: Workspace };

const stepTerminal = new Set([
  "succeeded",
  "failed",
  "canceled",
  "interrupted",
  "skipped",
]);
const attemptTerminal = new Set([
  "succeeded",
  "failed",
  "canceled",
  "interrupted",
  "uncertain",
  "blocked",
]);

/** A persisted run only moves forward; a stale HTTP snapshot must not revive it. */
function mergeRun(current: Run, incoming: Run): Run {
  return terminal(current.status) && !terminal(incoming.status)
    ? current
    : incoming;
}

function mergeStep(current: Step, incoming: Step): Step {
  return stepTerminal.has(current.status) && !stepTerminal.has(incoming.status)
    ? current
    : incoming;
}

function mergeAttempt(current: Attempt, incoming: Attempt): Attempt {
  return attemptTerminal.has(current.status) &&
    !attemptTerminal.has(incoming.status)
    ? current
    : incoming;
}

/**
 * Steps and attempts are append-only within a run, and an event-history page
 * can be a partial window. Merge into the known items instead of replacing
 * them, so a bounded seed or a stale snapshot never drops or repeats records.
 */
function mergeByID<T extends { id: string }>(
  current: T[] | undefined,
  incoming: T[],
  merge: (current: T, incoming: T) => T,
) {
  const items = new Map<string, T>();
  for (const item of current ?? []) if (item.id) items.set(item.id, item);
  for (const item of incoming) {
    if (!item.id) continue;
    const previous = items.get(item.id);
    items.set(item.id, previous ? merge(previous, item) : item);
  }
  return [...items.values()];
}

function withRunWarnings(
  current: Record<string, Event[]>,
  runID: string,
  warnings: Event[],
) {
  if (!warnings.length) {
    if (!(runID in current)) return current;
    const copy = { ...current };
    delete copy[runID];
    return copy;
  }
  return current[runID] === warnings
    ? current
    : { ...current, [runID]: warnings };
}

/**
 * A refresh may return a snapshot started before the last SSE frame. Merge
 * message bodies by id and keep runs terminal, so a reload cannot move the
 * loaded page backwards.
 */
function mergeThreadView(
  current: ThreadView | undefined,
  loaded: ThreadView,
  samePage: boolean,
  startConfirmed: Record<string, number>,
  messageWatermark: Record<string, number>,
): ThreadView {
  if (!current || current.thread.id !== loaded.thread.id) return loaded;
  const known = new Map(current.messages.items.map((item) => [item.id, item]));
  const seen = new Set<string>();
  const items = loaded.messages.items.map((message) => {
    seen.add(message.id);
    const previous = known.get(message.id);
    if (!previous) return message;
    // Only a message event that landed after this request started can be
    // newer than the consistent snapshot the server just returned.
    const applied = messageWatermark[message.id] ?? 0;
    const base = startConfirmed[message.run_id] ?? 0;
    return applied > base ? mergeMessage(previous, message) : message;
  });
  if (samePage)
    for (const message of current.messages.items) {
      if (seen.has(message.id)) continue;
      const applied = messageWatermark[message.id] ?? 0;
      const base = startConfirmed[message.run_id] ?? 0;
      if (applied > base) items.push(message);
    }
  return {
    ...loaded,
    messages: { ...loaded.messages, items },
    runs: loaded.runs.map((run) => {
      const previous = current.runs.find((item) => item.id === run.id);
      return previous ? mergeRun(previous, run) : run;
    }),
  };
}

const eventObject = (event: Event) =>
  event.data && typeof event.data === "object"
    ? (event.data as Record<string, unknown>)
    : {};

function eventRun(event: Event) {
  if (!event.type.startsWith("run_")) return undefined;
  const data = eventObject(event);
  return typeof data.id === "string" && typeof data.status === "string"
    ? (data as unknown as Run)
    : undefined;
}

function eventStep(event: Event) {
  if (!event.type.startsWith("step_")) return undefined;
  const data = eventObject(event);
  return typeof data.id === "string" &&
    data.id !== "" &&
    typeof data.status === "string"
    ? (data as unknown as Step)
    : undefined;
}

function eventAttempt(event: Event) {
  if (!event.type.startsWith("attempt_")) return undefined;
  const data = eventObject(event);
  return typeof data.id === "string" &&
    data.id !== "" &&
    typeof data.status === "string"
    ? (data as unknown as Attempt)
    : undefined;
}

export function App() {
  const [system, setSystem] = useState<System>();
  const [required, setRequired] = useState(false);
  const [error, setError] = useState("");
  const connect = useCallback(async () => {
    setError("");
    try {
      const info = await api<System>("/api/v1/system");
      setCSRF(info.csrf_token);
      setSystem(info);
      setRequired(false);
    } catch (error) {
      if (error instanceof APIError && error.status === 401) setRequired(true);
      else setError(errorText(error));
    }
  }, []);
  useEffect(() => {
    const lock = () => {
      setCSRF("");
      setSystem(undefined);
      setRequired(true);
      try {
        sessionStorage.removeItem("cocogem-drafts");
      } catch {
        // Nothing to clear when browser storage is unavailable.
      }
    };
    window.addEventListener("cocogem:unauthorized", lock);
    void connect();
    return () => window.removeEventListener("cocogem:unauthorized", lock);
  }, [connect]);
  if (!system)
    return <AuthScreen required={required} error={error} connect={connect} />;
  return (
    <WorkspaceApp
      initialSystem={system}
      lock={async () => {
        await api("/api/v1/auth/logout", "POST", {});
        setCSRF("");
        setSystem(undefined);
        setRequired(true);
        try {
          sessionStorage.removeItem("cocogem-drafts");
        } catch {
          // Nothing to clear when browser storage is unavailable.
        }
      }}
    />
  );
}

function WorkspaceApp({
  initialSystem,
  lock,
}: {
  initialSystem: System;
  lock: () => Promise<void>;
}) {
  const [appearance, setAppearance] = useState<Appearance>(
    () =>
      (document.documentElement.dataset.appearance as Appearance) || "system",
  );
  const changeAppearance = (value: Appearance) => {
    setAppearance(value);
    document.documentElement.dataset.appearance = value;
    try {
      localStorage.setItem("cocogem-appearance", value);
    } catch {
      // The current choice still applies if browser storage is unavailable.
    }
  };
  const [system, setSystem] = useState<System>(initialSystem);
  const [workspaces, setWorkspaces] = useState<Workspace[]>([]);
  const [workspaceID, setWorkspaceID] = useState(pathSelection().workspace);
  const [threadID, setThreadID] = useState(pathSelection().thread);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [globalAgents, setGlobalAgents] = useState<Agent[]>([]);
  const [settings, setSettings] = useState<Settings>();
  const [threads, setThreads] = useState<Thread[]>([]);
  const [view, setView] = useState<ThreadView>();
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [stepsByRun, setStepsByRun] = useState<Record<string, Step[]>>({});
  const [attemptsByRun, setAttemptsByRun] = useState<
    Record<string, Attempt[]>
  >({});
  const [events, setEvents] = useState<Event[]>([]);
  const [livenessByRun, setLivenessByRun] = useState<Record<string, Event[]>>(
    {},
  );
  const [messageCursor, setMessageCursor] = useState("");
  const [messagePaging, setMessagePaging] = useState(false);
  const [sourceID, setSourceID] = useState("");
  const [sourceLoading, setSourceLoading] = useState(false);
  const [error, setError] = useState("");
  const [ready, setReady] = useState(false);
  const [busy, setBusy] = useState(false);
  const [modal, setModal] = useState<ModalState | null>(null);
  const [drafts, setDrafts] = useState<Record<string, string>>(() => {
    try {
      const stored = sessionStorage.getItem("cocogem-drafts");
      const parsed = stored ? (JSON.parse(stored) as unknown) : null;
      return parsed && typeof parsed === "object" && !Array.isArray(parsed)
        ? (parsed as Record<string, string>)
        : {};
    } catch {
      return {};
    }
  });
  const [workflowChoices, setWorkflowChoices] = useState<
    Record<string, WorkflowKey>
  >({});
  const [routing, setRouting] = useState<Routing>();
  const [tab, setTab] = useState<PanelTab>("artifacts");
  const [source, setSource] = useState<Source>();
  const [artifact, setArtifact] = useState<Artifact>();
  const [artifactRef, setArtifactRef] = useState<Artifact>();
  const [connection, setConnection] = useState("");
  const [newMessages, setNewMessages] = useState(false);
  const [pendingRequest, setPendingRequest] = useState(false);
  const [navOpen, setNavOpen] = useState(false);
  const [panelOpen, setPanelOpen] = useState(false);
  const [inspectedRunID, setInspectedRunID] = useState("");
  // Live streaming waits until the selected run's recent window is seeded.
  const [seededRunID, setSeededRunID] = useState("");
  const timeline = useRef<HTMLDivElement>(null);
  const follows = useRef(true);
  const currentThread = useRef(threadID);
  const currentWorkspace = useRef(workspaceID);
  currentWorkspace.current = workspaceID;
  currentThread.current = threadID;
  const refreshGeneration = useRef(0);
  const sourceGeneration = useRef(0);
  // Page browsing replaces the page instead of accumulating all history.
  const messageCursorRef = useRef("");
  const viewCursor = useRef("");
  // These stay outside the bounded event window so reconnect/eviction cannot
  // reorder SSE or erase a liveness warning.
  const confirmedSeq = useRef<Record<string, number>>({});
  const appliedDelta = useRef<Record<string, number>>({});
  const snapshotSeq = useRef<Record<string, number>>({});
  // Sequence of the last message event actually merged, per message id.
  const messageWatermark = useRef<Record<string, number>>({});
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
  const latestLiveness = inspectedRun
    ? livenessByRun[inspectedRun.id]
    : undefined;
  const active = runs.some((run) => !terminal(run.status));
  const available = agents.filter((agent) => !agent.disabled);
  const newThreadMembers = [...available].sort(
    (a, b) =>
      Number(b.id === settings?.default_coordinator_id) -
      Number(a.id === settings?.default_coordinator_id),
  );
  const draftKey = threadID || `new:${workspaceID}`;
  const draft = drafts[draftKey] ?? "";
  const threadMembers = view
    ? available.filter((agent) => view.thread.agent_ids?.includes(agent.id))
    : threadID
      ? []
      : newThreadMembers.slice(0, 4);
  const workflow =
    workflowChoices[draftKey] ??
    (view?.thread.workflow as WorkflowKey | undefined) ??
    "free";
  const setWorkflow = (value: WorkflowKey) => {
    setWorkflowChoices((current) => ({ ...current, [draftKey]: value }));
    setRouting(undefined);
  };
  // Existing conversations keep their own coordinator; a new one previews the
  // same default/fallback the server applies when creating it.
  const coordinatorID = threadID
    ? (view?.thread.coordinator_id ?? "")
    : conversationCoordinator(
        threadMembers,
        settings?.default_coordinator_id ?? "",
      );
  const coordinatorMember = agents.find((agent) => agent.id === coordinatorID);
  const coordinator = coordinatorMember
    ? {
        name: coordinatorMember.display_name,
        fallback:
          !threadID &&
          !!settings?.default_coordinator_id &&
          coordinatorID !== settings.default_coordinator_id,
      }
    : undefined;
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
    setLivenessByRun({});
    setInspectedRunID("");
    setSeededRunID("");
    setStepsByRun({});
    setAttemptsByRun({});
    settledSteps.current.clear();
    setArtifacts([]);
    setSource(undefined);
    setSourceID("");
    setSourceLoading(false);
    sourceGeneration.current++;
    setArtifactRef(undefined);
    setRouting(undefined);
    messageCursorRef.current = "";
    viewCursor.current = "";
    setMessageCursor("");
    setMessagePaging(false);
    follows.current = true;
    confirmedSeq.current = {};
    appliedDelta.current = {};
    snapshotSeq.current = {};
    messageWatermark.current = {};
    if (push)
      history.pushState({}, "", ws ? `/w/${ws}${th ? `/t/${th}` : ""}` : "/");
  }, []);
  const loadSpaces = useCallback(async () => {
    const result = await api<{ items: Workspace[] }>("/api/v1/workspaces");
    setWorkspaces(result.items ?? []);
    return result.items ?? [];
  }, []);
  const loadGlobalAgents = useCallback(async () => {
    const result = await api<{ items: Agent[] }>("/api/v1/agents");
    setGlobalAgents(result.items ?? []);
  }, []);
  const loadSettings = useCallback(async () => {
    setSettings(await api<Settings>("/api/v1/settings"));
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
  const refreshThread = useCallback(
    async (id: string, cursor = messageCursorRef.current) => {
      const generation = ++refreshGeneration.current;
      // Anything applied after this point is newer than the HTTP snapshot.
      const startConfirmed = { ...confirmedSeq.current };
      const result = await loadThreadPage(id, cursor);
      const deliverables = await api<{ items: Artifact[] }>(
        `/api/v1/threads/${id}/artifacts`,
      );
      if (
        currentThread.current !== id ||
        generation !== refreshGeneration.current
      )
        return;
      const samePage = cursor === viewCursor.current;
      setView((current) =>
        mergeThreadView(
          current,
          result,
          samePage,
          startConfirmed,
          messageWatermark.current,
        ),
      );
      viewCursor.current = cursor;
      setArtifacts(deliverables.items ?? []);
      const stale = result.runs.filter(
        (run) => !settledSteps.current.has(run.id),
      );
      const loaded = await Promise.all(
        stale.map((run) => api<RunView>(`/api/v1/runs/${run.id}`)),
      );
      if (
        currentThread.current !== id ||
        generation !== refreshGeneration.current
      )
        return;
      for (const item of loaded) {
        if (terminal(item.run.status)) settledSteps.current.add(item.run.id);
        // A run view's attempt snapshots are the current liveness truth; the
        // bounded event window may no longer contain the warning.
        const attempts = item.attempts ?? [];
        if (attempts.length)
          setLivenessByRun((current) =>
            withRunWarnings(
              current,
              item.run.id,
              mergeAttemptLiveness(
                current[item.run.id] ?? [],
                attempts,
                item.run.id,
              ),
            ),
          );
      }
      setStepsByRun((current) => ({
        ...current,
        ...Object.fromEntries(
          loaded.map((item) => [
            item.run.id,
            mergeByID(current[item.run.id], item.steps ?? [], mergeStep),
          ]),
        ),
      }));
      setAttemptsByRun((current) => ({
        ...current,
        ...Object.fromEntries(
          loaded.map((item) => [
            item.run.id,
            mergeByID(current[item.run.id], item.attempts ?? [], mergeAttempt),
          ]),
        ),
      }));
    },
    [],
  );

  useEffect(() => {
    let canceled = false;
    async function initialize() {
      try {
        const [spaces] = await Promise.all([
          loadSpaces(),
          loadGlobalAgents(),
          loadSettings(),
        ]);
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
    // Initialization runs once for each unlocked session.
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
        if (panelOpen) document.getElementById("context-toggle")?.focus();
      }
    };
    addEventListener("keydown", escape);
    return () => removeEventListener("keydown", escape);
  }, [navOpen, panelOpen]);
  useEffect(() => {
    try {
      const kept = Object.fromEntries(
        Object.entries(drafts).filter(([, value]) => value !== ""),
      );
      if (Object.keys(kept).length) {
        sessionStorage.setItem("cocogem-drafts", JSON.stringify(kept));
      } else {
        sessionStorage.removeItem("cocogem-drafts");
      }
    } catch {
      // Drafts still work in memory when browser storage is unavailable.
    }
  }, [drafts]);
  useEffect(() => {
    if (ready && workspaceID) {
      void Promise.all([
        loadMembers(workspaceID),
        loadThreads(workspaceID),
        loadGlobalAgents(),
      ]).catch((error) => setError(errorText(error)));
    } else if (ready) {
      setAgents([]);
      setThreads([]);
    }
  }, [ready, workspaceID, loadMembers, loadThreads, loadGlobalAgents]);
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
    let canceled = false;
    async function seed() {
      // The run view cursor is the end of the replay window, not proof that the
      // loaded message page already contains those deltas. Apply the seed
      // snapshots first and only then advance the confirmed high-water mark.
      let cursor = 0;
      let snapshotWarnings: Event[] | undefined;
      try {
        const detail = await api<RunView>(`/api/v1/runs/${id}`);
        if (typeof detail.cursor === "number")
          cursor = Math.max(cursor, detail.cursor);
        if (canceled) return;
        if (detail.run?.id === id) {
          const snapshot = detail.run;
          setView((current) => {
            if (!current || current.thread.id !== thread) return current;
            const runs = current.runs.map((run) =>
              run.id === id ? mergeRun(run, snapshot) : run,
            );
            return runs.every((run, index) => run === current.runs[index])
              ? current
              : { ...current, runs };
          });
          if (terminal(snapshot.status)) settledSteps.current.add(id);
        }
        if (Array.isArray(detail.steps))
          setStepsByRun((current) => ({
            ...current,
            [id]: mergeByID(current[id], detail.steps ?? [], mergeStep),
          }));
        const detailAttempts = detail.attempts ?? [];
        if (Array.isArray(detail.attempts)) {
          setAttemptsByRun((current) => ({
            ...current,
            [id]: mergeByID(current[id], detailAttempts, mergeAttempt),
          }));
          // Snapshots restore warnings older than the recent event window and
          // a missing warning is the backend's clear signal.
          if (detailAttempts.length) {
            snapshotWarnings = livenessFromAttempts(detailAttempts, id);
            setLivenessByRun((current) =>
              withRunWarnings(
                current,
                id,
                mergeAttemptLiveness(current[id] ?? [], detailAttempts, id),
              ),
            );
          }
        }
      } catch (error) {
        if (canceled) return;
        setError(errorText(error));
      }
      if (canceled) return;
      // Seed only the recent window; full history stays in EventHistory.
      const after = Math.max(0, cursor - EVENT_WINDOW);
      try {
        const page = await eventHistory(
          id,
          after ? String(after) : "",
          EVENT_WINDOW,
        );
        if (canceled) return;
        const items = [...(page.items ?? [])].sort((a, b) => a.seq - b.seq);
        let newest = 0;
        let seeded: Event[] = [];
        const messages: { seq: number; message: Message }[] = [];
        const steps = new Map<string, Step>();
        const attempts = new Map<string, Attempt>();
        let runSnapshot: Run | undefined;
        for (const event of items) {
          newest = Math.max(newest, event.seq);
          seeded = mergeEvents(seeded, event);
          if (event.run_id !== id) continue;
          const message = eventMessage(event);
          if (message && message.run_id === id)
            messages.push({ seq: event.seq, message });
          const run = eventRun(event);
          if (run?.id === id)
            runSnapshot = runSnapshot
              ? mergeRun(runSnapshot, run)
              : run;
          const step = eventStep(event);
          if (step?.run_id === id) {
            const previous = steps.get(step.id);
            steps.set(step.id, previous ? mergeStep(previous, step) : step);
          }
          const attempt = eventAttempt(event);
          if (attempt?.run_id === id) {
            const previous = attempts.get(attempt.id);
            attempts.set(
              attempt.id,
              previous ? mergeAttempt(previous, attempt) : attempt,
            );
          }
        }
        setEvents(seeded);
        const warningsSnapshot = snapshotWarnings;
        setLivenessByRun((current) => {
          const runEvents = items.filter((event) => event.run_id === id);
          // Attempt snapshots were taken at cursor; older warnings in the
          // replay window are already reflected (or cleared) there.
          const warnings =
            warningsSnapshot !== undefined
              ? seedLiveness(
                  warningsSnapshot,
                  runEvents.filter((event) => event.seq > cursor),
                )
              : seedLiveness(current[id] ?? [], runEvents);
          return withRunWarnings(current, id, warnings);
        });
        if (messages.length)
          setView((current) => {
            if (!current || current.thread.id !== thread) return current;
            const index = new Map(
              current.messages.items.map((message, at) => [message.id, at]),
            );
            const next = current.messages.items.slice();
            for (const { message } of messages) {
              const at = index.get(message.id);
              if (at !== undefined) next[at] = mergeMessage(next[at], message);
              else if (messageCursorRef.current === "") {
                index.set(message.id, next.length);
                next.push(message);
              }
            }
            const changed =
              next.length !== current.messages.items.length ||
              next.some((message, at) => message !== current.messages.items[at]);
            return changed
              ? { ...current, messages: { ...current.messages, items: next } }
              : current;
          });
        for (const { seq, message } of messages)
          messageWatermark.current[message.id] = Math.max(
            messageWatermark.current[message.id] ?? 0,
            seq,
          );
        if (runSnapshot)
          setView((current) => {
            if (!current || current.thread.id !== thread) return current;
            const runs = current.runs.map((run) =>
              run.id === id ? mergeRun(run, runSnapshot) : run,
            );
            return runs.every((run, index) => run === current.runs[index])
              ? current
              : { ...current, runs };
          });
        if (steps.size)
          setStepsByRun((current) => ({
            ...current,
            [id]: mergeByID(current[id], [...steps.values()], mergeStep),
          }));
        if (attempts.size)
          setAttemptsByRun((current) => ({
            ...current,
            [id]: mergeByID(current[id], [...attempts.values()], mergeAttempt),
          }));
        confirmedSeq.current[id] = Math.max(
          confirmedSeq.current[id] ?? 0,
          cursor,
          newest,
        );
        // Only events actually merged into the loaded page may suppress later
        // deltas; the run view cursor alone is not a message watermark.
        snapshotSeq.current[id] = Math.max(
          snapshotSeq.current[id] ?? 0,
          newest,
        );
      } catch (error) {
        if (canceled) return;
        // Streaming still starts from the snapshot high-water mark; older
        // events remain available through the on-demand history panel.
        setError(errorText(error));
        confirmedSeq.current[id] = Math.max(
          confirmedSeq.current[id] ?? 0,
          cursor,
        );
      } finally {
        if (!canceled) {
          setSeededRunID(id);
          // One bounded refresh makes the thread snapshot agree with the run
          // view without polling; later frames still merge by message id.
          void refreshThread(thread).catch((error) =>
            setError(errorText(error)),
          );
        }
      }
    }
    void seed();
    return () => {
      canceled = true;
    };
  }, [inspectedRun?.id, threadID, refreshThread]);
  useEffect(() => {
    if (!inspectedRun || seededRunID !== inspectedRun.id) return;
    // Finished runs have nothing to stream; their history stays paged.
    if (terminal(inspectedRun.status)) return;
    const id = inspectedRun.id;
    const thread = threadID;
    const after = confirmedSeq.current[id] ?? 0;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const stream = new EventSource(
      `/api/v1/runs/${id}/events${after ? `?after=${after}` : ""}`,
    );
    setConnection("正在连接");
    stream.onopen = () => setConnection("实时连接");
    stream.onmessage = (message) => {
      const event = JSON.parse(message.data) as Event;
      // Replayed or cross-run frames must not move the cursor, apply a delta,
      // clear a warning, or trigger a reload.
      if (
        event.run_id !== id ||
        event.seq <= (confirmedSeq.current[id] ?? 0)
      )
        return;
      confirmedSeq.current[id] = event.seq;
      setEvents((current) => mergeEvents(current, event));
      setLivenessByRun((current) => {
        const previous = current[event.run_id];
        const next = nextLiveness(previous ?? [], event);
        return withRunWarnings(current, event.run_id, next);
      });
      // Replayed deltas predate the loaded snapshot; only newer ones update it.
      if (
        event.type === "message_delta" &&
        event.seq > (appliedDelta.current[event.run_id] ?? 0) &&
        event.seq > (snapshotSeq.current[event.run_id] ?? Number.MAX_SAFE_INTEGER)
      ) {
        appliedDelta.current[event.run_id] = event.seq;
        const data =
          event.data && typeof event.data === "object"
            ? (event.data as Record<string, unknown>)
            : {};
        const messageID =
          typeof data.message_id === "string" ? data.message_id : "";
        if (messageID) messageWatermark.current[messageID] = event.seq;
        setView((current) => {
          if (!current) return current;
          const items = applyMessageDelta(current.messages.items, event);
          return items === current.messages.items
            ? current
            : { ...current, messages: { ...current.messages, items } };
        });
      }
      if (!timer)
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
    stream.onerror = () => {
      setConnection("连接中断，正在重新连接");
      void api<System>("/api/v1/system").catch((error) => {
        if (!(error instanceof APIError && error.status === 401))
          setError(errorText(error));
      });
    };
    return () => {
      stream.close();
      if (timer) clearTimeout(timer);
    };
  }, [
    inspectedRun?.id,
    inspectedRun?.status,
    seededRunID,
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
    } else if (!messageCursorRef.current) setNewMessages(true);
  }, [view?.messages.items, stepsByRun]);

  async function createThread(text: string) {
    if (!workspaceID || available.length < 1)
      throw new Error("请先创建工作空间和可用成员");
    const thread = await api<Thread>(
      `/api/v1/workspaces/${workspaceID}/threads`,
      "POST",
      {
        title: threadTitle(text),
        // The server applies the global default coordinator when omitted.
        agent_ids: newThreadMembers.slice(0, 4).map((agent) => agent.id),
        workflow,
      },
    );
    currentThread.current = thread.id;
    setWorkflowChoices((current) => ({ ...current, [thread.id]: workflow }));
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
            targets: [],
            // The server resolves global/session defaults from an empty map.
            assignments: {},
            source_urls: [],
            artifact_refs: artifactRef
              ? [{ id: artifactRef.id, version: artifactRef.version }]
              : [],
            research_scope: "auto",
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
      setLivenessByRun({});
      setInspectedRunID("");
      setSeededRunID("");
      setArtifactRef(undefined);
      messageCursorRef.current = "";
      viewCursor.current = "";
      setMessageCursor("");
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
  async function resume(runID: string) {
    if (busy || active) return;
    setBusy(true);
    setError("");
    try {
      await api(`/api/v1/runs/${runID}/resume`, "POST", {});
      settledSteps.current.delete(runID);
      follows.current = true;
      messageCursorRef.current = "";
      viewCursor.current = "";
      setMessageCursor("");
      await refreshThread(threadID);
      await loadThreads(workspaceID);
    } catch (error) {
      setError(errorText(error));
    } finally {
      setBusy(false);
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
    const id = typeof value === "string" ? value : value.id;
    setSourceID(id);
    // The list only carries metadata; always read the captured body here.
    const generation = ++sourceGeneration.current;
    setSource(undefined);
    setSourceLoading(true);
    void api<Source>(`/api/v1/sources/${id}`)
      .then((loaded) => {
        if (generation === sourceGeneration.current) setSource(loaded);
      })
      .catch((error) => {
        if (generation === sourceGeneration.current)
          setError(errorText(error));
      })
      .finally(() => {
        if (generation === sourceGeneration.current) setSourceLoading(false);
      });
  };
  async function browseMessages(cursor: string) {
    if (!threadID || messagePaging) return;
    const previous = messageCursorRef.current;
    const latest = cursor === "";
    if (latest) follows.current = true;
    else follows.current = false;
    messageCursorRef.current = cursor;
    setMessageCursor(cursor);
    setMessagePaging(true);
    try {
      await refreshThread(threadID, cursor);
    } catch (error) {
      messageCursorRef.current = previous;
      setMessageCursor(previous);
      setError(errorText(error));
    } finally {
      setMessagePaging(false);
    }
  }
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
            请确认本地服务仍在运行。
          </p>
        ) : (
          <p>正在连接本地工作空间…</p>
        )}
      </main>
    );

  const messages = view?.messages.items ?? [];
  return (
    <div className={cx("app-shell")}>
      <a className={cx("skip-link")} href="#composer-input">
        跳到输入框
      </a>
      <Sidebar
        appearance={appearance}
        onAppearance={changeAppearance}
        open={navOpen}
        close={() => setNavOpen(false)}
        workspaces={workspaces}
        workspaceID={workspaceID}
        workspace={workspace}
        threads={threads}
        threadID={threadID}
        onWorkspace={(id) => navigate(id)}
        onHome={() => navigate(workspaceID)}
        onThread={(id) => navigate(workspaceID, id)}
        onNewThread={() => setModal({ kind: "thread" })}
        onRenameThread={(thread) => setModal({ kind: "rename-thread", thread })}
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
            {view ? (
              <>
                <span>
                  {workspace?.name} /{" "}
                  {workflows[view.thread.workflow as WorkflowKey] ||
                    workflows.free}
                </span>
                <h1>{view.thread.title}</h1>
              </>
            ) : (
              <strong>{workspace?.name ?? "Cocogem"}</strong>
            )}
          </div>
          <div className={cx("top-actions")}>
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
              className={cx("ghost desktop-only")}
              onClick={() => setModal({ kind: "agents" })}
              disabled={!workspace}
              aria-label={`成员 ${agents.length}`}
            >
              <span className={cx("avatar-stack")}>
                {agents.slice(0, 4).map((agent) => (
                  <Avatar
                    key={agent.id}
                    name={agent.display_name}
                    id={agent.id}
                    avatar={agent.avatar}
                    small
                  />
                ))}
              </span>
              {agents.length} 位成员
            </button>
            <button
              className={cx("ghost panel-toggle")}
              id="context-toggle"
              aria-label={panelOpen ? "收起协作面板" : "打开协作面板"}
              aria-expanded={panelOpen}
              aria-controls="collaboration-panel"
              onClick={() => setPanelOpen((current) => !current)}
            >
              <Icon name="panel" size={16} />
              <span>协作</span>
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
            <div className={cx("timeline-wrap")}>
              <div
                className={cx("timeline")}
                data-testid="conversation-timeline"
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
                  {view &&
                    (messageCursor || view.messages.next_cursor) && (
                      <div
                        className={cx("timeline-paging")}
                        role="group"
                        aria-label="消息分页"
                      >
                        {messageCursor && (
                          <button
                            type="button"
                            className={cx("ghost small")}
                            disabled={messagePaging}
                            onClick={() => void browseMessages("")}
                          >
                            回到最新消息
                          </button>
                        )}
                        {view.messages.next_cursor && (
                          <button
                            type="button"
                            className={cx("ghost small")}
                            disabled={messagePaging}
                            onClick={() =>
                              void browseMessages(
                                view.messages.next_cursor,
                              )
                            }
                          >
                            加载更早的消息
                          </button>
                        )}
                        {messagePaging && (
                          <small className={cx("muted")} role="status">
                            正在加载消息…
                          </small>
                        )}
                      </div>
                    )}
                  {runs.map((run, index) => (
                    <RunSegment
                      key={run.id}
                      run={run}
                      index={index + 1}
                      steps={stepsByRun[run.id]}
                      attempts={attemptsByRun[run.id]}
                      messages={messages}
                      sources={view?.sources ?? []}
                      artifacts={artifacts}
                      members={agents}
                      connection={run.id === inspectedRun?.id ? connection : ""}
                      liveness={
                        run.id === inspectedRun?.id ? latestLiveness : undefined
                      }
                      events={run.id === inspectedRun?.id ? events : undefined}
                      latest={run.id === currentRun?.id}
                      busy={busy}
                      onSnapshot={() => setModal({ kind: "snapshot", run })}
                      onRetry={() =>
                        void send({ ...run.input, retry_of_run_id: run.id })
                      }
                      onResume={() => void resume(run.id)}
                      onRefresh={() =>
                        void refreshThread(threadID).catch((error) =>
                          setError(errorText(error)),
                        )
                      }
                      onSource={openSource}
                      onArtifact={(selected) => {
                        setArtifact(selected);
                        setModal({ kind: "artifact" });
                      }}
                    />
                  ))}
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
              artifactRef={artifactRef}
              clearArtifactRef={() => setArtifactRef(undefined)}
              coordinator={coordinator}
              routing={routing}
              active={!!active}
              canceling={currentRun?.status === "canceling"}
              busy={busy}
              disabled={!workspace}
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
            close={() => {
              setPanelOpen(false);
              document.getElementById("context-toggle")?.focus();
            }}
            tab={tab}
            setTab={setTab}
            sources={view?.sources ?? []}
            source={source}
            sourceID={sourceID}
            sourceLoading={sourceLoading}
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
              confirmedSeq.current[id] = 0;
              appliedDelta.current[id] = 0;
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
                  skill_ids: value.skill_ids,
                },
                value.id ? { "If-Match": String(value.revision) } : {},
              );
              await loadSpaces();
              close();
              if (!value.id) navigate(saved.id);
            }}
          />
        )}
      {modal?.kind === "agents" && workspaceID && (
        <WorkspaceMembersForm
          agents={agents}
          catalog={globalAgents}
          workspaceID={workspaceID}
          refresh={() => loadMembers(workspaceID)}
          close={close}
        />
      )}
      {(modal?.kind === "thread" || modal?.kind === "edit-thread") &&
        workspace && (
          <ThreadForm
            current={modal.kind === "edit-thread" ? view?.thread : undefined}
            agents={available}
            defaultCoordinatorID={settings?.default_coordinator_id}
            close={close}
            save={async (value) => {
              const th = await api<Thread>(
                value.id
                  ? `/api/v1/threads/${value.id}`
                  : `/api/v1/workspaces/${workspaceID}/threads`,
                value.id ? "PATCH" : "POST",
                {
                  title: value.title,
                  agent_ids: value.agent_ids,
                  coordinator_id: value.coordinator_id,
                  workflow: value.workflow,
                },
                value.id ? { "If-Match": String(value.revision) } : {},
              );
              setWorkflowChoices((current) => ({
                ...current,
                [th.id]: th.workflow as WorkflowKey,
              }));
              if (value.id)
                setView((current) =>
                  current?.thread.id === th.id
                    ? { ...current, thread: th }
                    : current,
                );
              close();
              await loadThreads(workspaceID);
              if (!value.id) navigate(workspaceID, th.id);
            }}
          />
        )}
      {modal?.kind === "snapshot" && (
        <SnapshotDialog run={modal.run} close={close} />
      )}
      {modal?.kind === "settings" && system && (
        <SettingsDialog
          system={system}
          setSystem={setSystem}
          close={close}
          lock={lock}
          settings={settings}
          saveSettings={async (value) => {
            const saved = await api<Settings>(
              "/api/v1/settings",
              "PATCH",
              value,
              { "If-Match": String(settings?.revision ?? 0) },
            );
            setSettings(saved);
            return saved;
          }}
          agents={globalAgents}
          saveAgent={async (value) => {
            const saved = await api<Agent>(
              value.id
                ? `/api/v1/agents/${value.id}`
                : "/api/v1/agents",
              value.id ? "PATCH" : "POST",
              value,
              value.id ? { "If-Match": String(value.revision) } : {},
            );
            await loadGlobalAgents();
            if (workspaceID) await loadMembers(workspaceID);
            return saved;
          }}
        />
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
      {modal?.kind === "rename-thread" && (
        <ThreadRenameForm
          thread={modal.thread}
          close={close}
          save={async (title) => {
            // The title-only update leaves members, coordinator and workflow untouched.
            const saved = await api<Thread>(
              `/api/v1/threads/${modal.thread.id}`,
              "PATCH",
              { title },
              { "If-Match": String(modal.thread.revision) },
            );
            setView((current) =>
              current?.thread.id === saved.id
                ? { ...current, thread: saved }
                : current,
            );
            close();
            await loadThreads(workspaceID);
          }}
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
            个会话、成员关联、来源和产物会被永久删除，无法恢复。全局成员档案会保留。
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
            <li>创建工作空间，选择可以跨空间复用的成员</li>
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
        直接提出问题，也可以附上链接。成员会主动查证、交流与复核，把过程和结论留在这里。
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
        {members} 位成员已就位。可以描述你需要的分工，或输入 @ 指定成员。
      </p>
    </div>
  );
}
