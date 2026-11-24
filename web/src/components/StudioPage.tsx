import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  workspaceStudio,
  type StudioArtifact,
  type StudioSnapshot,
  type Workspace,
} from "../api/client";
import { resolveStudioCat } from "../lib/cats";
import { t, useLanguage } from "../lib/i18n";
import { errorText } from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import studio from "../styles/studio.module.css";
import { Avatar } from "./Avatar";
import { Icon } from "./Icon";
import { Modal } from "./Modal";
import {
  PermissionDecision,
  submitPermissionDecision,
} from "./PermissionDecision";
import { permissionPrompt } from "./PermissionQueue";
import { Select } from "./Select";
import { StudioCommand } from "./StudioCommand";
import {
  STUDIO_PET_EMOTES,
  QUICK_SPOTS,
  StudioScene,
  poseDescription,
  stateKind,
  stateLabel,
  useStudioCats,
  type Spot,
} from "./StudioScene";

const cx = scoped(base, controls, studio);

type Emote = { id: number; text: string };
type Permission = StudioSnapshot["pending_permissions"][number];

function without<T>(record: Record<string, T>, key: string) {
  const copy = { ...record };
  delete copy[key];
  return copy;
}

export function StudioPage({
  workspaces,
  workspaceID,
  onWorkspace,
  onOpenThread,
  onDispatch,
}: {
  workspaces: Workspace[];
  workspaceID: string;
  onWorkspace: (id: string) => void;
  onOpenThread: (workspaceID: string, threadID: string) => void;
  onDispatch: (text: string, threadID?: string) => Promise<string | undefined>;
}) {
  const [lang] = useLanguage();
  const [loaded, setSnapshot] = useState<StudioSnapshot | null>(null);
  const snapshot = loaded?.workspace_id === workspaceID ? loaded : null;
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [focusedThreadID, setFocusedThreadID] = useState("");
  const [selectedAgentID, setSelectedAgentID] = useState("");
  const [selectedArtifact, setSelectedArtifact] =
    useState<StudioArtifact | null>(null);
  const [approval, setApproval] = useState<Permission>();
  const [commandError, setCommandError] = useState("");
  const [sending, setSending] = useState(false);
  const [panelOpen, setPanelOpen] = useState(true);
  const [reacting, setReacting] = useState<Record<string, number>>({});
  const [emotes, setEmotes] = useState<Record<string, Emote>>({});
  const [reducedMotion, setReducedMotion] = useState(
    () =>
      window.matchMedia?.("(prefers-reduced-motion: reduce)")?.matches ?? false,
  );
  const emoteSeq = useRef(1);
  const loadGeneration = useRef(0);
  const loadBusy = useRef(false);
  const dispatching = useRef(false);

  const load = useCallback(
    async (silent = false, refresh = false) => {
      if (!workspaceID || (loadBusy.current && !refresh)) return;
      const generation = ++loadGeneration.current;
      loadBusy.current = true;
      if (!silent) setLoading(true);
      try {
        const next = await workspaceStudio(workspaceID);
        if (generation !== loadGeneration.current) return;
        setSnapshot(next);
        setError("");
      } catch (failure) {
        if (generation === loadGeneration.current) setError(errorText(failure));
      } finally {
        if (generation === loadGeneration.current) {
          loadBusy.current = false;
          setLoading(false);
        }
      }
    },
    [workspaceID],
  );

  const visibleAgents = useMemo(() => {
    const list = (snapshot?.agents ?? []).filter((agent) => agent.enabled);
    if (!focusedThreadID) return list;
    return list.map((agent) =>
      agent.thread_id === focusedThreadID
        ? agent
        : { ...agent, state: "idle", station: "idle", activity_summary: "" },
    );
  }, [snapshot, focusedThreadID]);
  const { placed, sendTo, reset } = useStudioCats(visibleAgents, reducedMotion);

  useEffect(() => {
    setFocusedThreadID("");
    setSelectedAgentID("");
    setSelectedArtifact(null);
    setApproval(undefined);
    reset();
    void load(false);
    return () => {
      loadGeneration.current++;
      loadBusy.current = false;
    };
  }, [load, reset]);

  useEffect(() => {
    if (!workspaceID) return;
    const refresh = () => {
      if (document.visibilityState === "visible") void load(true);
    };
    const timer = window.setInterval(refresh, 3000);
    document.addEventListener("visibilitychange", refresh);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", refresh);
    };
  }, [workspaceID, load]);

  useEffect(() => {
    const media = window.matchMedia?.("(prefers-reduced-motion: reduce)");
    if (!media) return;
    const update = () => setReducedMotion(media.matches);
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);

  const visibleRelays = useMemo(
    () =>
      (snapshot?.relays ?? []).filter(
        (item) => !focusedThreadID || item.thread_id === focusedThreadID,
      ),
    [snapshot, focusedThreadID],
  );
  const visiblePermissions = useMemo(
    () =>
      (snapshot?.pending_permissions ?? []).filter(
        (item) => !focusedThreadID || item.request.thread_id === focusedThreadID,
      ),
    [snapshot, focusedThreadID],
  );
  const visibleArtifacts = useMemo(
    () =>
      (snapshot?.artifacts ?? []).filter(
        (item) => !focusedThreadID || item.thread_id === focusedThreadID,
      ),
    [snapshot, focusedThreadID],
  );
  const selectedAgent = visibleAgents.find(
    (agent) => agent.agent_id === selectedAgentID,
  );
  const focusedThread = snapshot?.threads.find(
    (item) => item.id === focusedThreadID,
  );
  const activeCount = visibleAgents.filter(
    (agent) => stateKind(agent.state) === "active",
  ).length;

  const react = useCallback(
    (agentID: string, custom?: string) => {
      const seq = emoteSeq.current++;
      const pool = STUDIO_PET_EMOTES[lang === "en" ? "en" : "zh"];
      const text = custom ?? pool[seq % pool.length];
      setReacting((cur) => ({ ...cur, [agentID]: seq }));
      setEmotes((cur) => ({ ...cur, [agentID]: { id: seq, text } }));
      window.setTimeout(
        () => setReacting((cur) => (cur[agentID] === seq ? without(cur, agentID) : cur)),
        600,
      );
      window.setTimeout(
        () => setEmotes((cur) => (cur[agentID]?.id === seq ? without(cur, agentID) : cur)),
        1400,
      );
    },
    [lang],
  );

  const pick = (agentID: string) => {
    setSelectedArtifact(null);
    setSelectedAgentID(agentID);
    setPanelOpen(true);
    react(agentID);
  };

  // A click on open floor sends the selected idle cat (or the first one) there.
  const onFloor = (spot: Spot) => {
    const cat =
      selectedAgent && stateKind(selectedAgent.state) === "idle"
        ? selectedAgent
        : visibleAgents.find((agent) => stateKind(agent.state) === "idle");
    if (!cat) return false;
    setSelectedArtifact(null);
    setSelectedAgentID(cat.agent_id);
    sendTo(cat.agent_id, spot);
    return true;
  };

  const handleCommand = async (text: string) => {
    if (dispatching.current) return false;
    dispatching.current = true;
    setSending(true);
    setCommandError("");
    try {
      const target = await onDispatch(text, focusedThreadID || undefined);
      if (target) {
        setFocusedThreadID((cur) => (cur === focusedThreadID ? target : cur));
      }
      void load(true, true);
      return true;
    } catch (failure) {
      setCommandError(errorText(failure));
      return false;
    } finally {
      dispatching.current = false;
      setSending(false);
    }
  };

  if (!workspaceID) {
    return (
      <div className={cx("studio-page")}>
        <div className={cx("studio-card studio-empty-state")}>
          <p className={cx("muted")}>
            {t(
              "先创建一个工作空间，猫猫研发室就会在这里亮灯开工。",
              "Create a workspace first to open the Pixel Cat R&D Studio.",
            )}
          </p>
        </div>
      </div>
    );
  }

  const pill =
    visiblePermissions.length > 0
      ? t(`${visiblePermissions.length} 项待审批`, `${visiblePermissions.length} pending`)
      : activeCount > 0
        ? t(`${activeCount} 只猫猫协作中`, `${activeCount} active`)
        : loading
          ? t("同步中", "Syncing")
          : error
            ? t("连接中断", "Connection lost")
            : t("在线", "Live");

  return (
    <div className={cx("studio-page")} data-testid="studio-page">
      <header className={cx("studio-topbar")} data-testid="studio-topbar">
        <div className={cx("studio-topbar-left")}>
          <span className={cx("studio-brand-crumb")}>
            <strong>Cocogem</strong>
            <span className={cx("studio-brand-sep")}>/</span>
            <span className={cx("studio-brand-sub")}>{t("猫猫研发室", "Studio")}</span>
          </span>
          {workspaces.length > 1 && (
            <Select
              className={cx("studio-select")}
              containerClassName={cx("studio-select-slot")}
              aria-label={t("切换研发室工作空间", "Switch studio workspace")}
              value={workspaceID}
              onChange={onWorkspace}
            >
              {workspaces.map((ws) => (
                <option key={ws.id} value={ws.id}>
                  {ws.name}
                </option>
              ))}
            </Select>
          )}
          <Select
            className={cx("studio-select")}
            containerClassName={cx("studio-select-slot")}
            aria-label={t("聚焦会话", "Focus conversation")}
            value={focusedThreadID}
            onChange={setFocusedThreadID}
          >
            <option value="">{t("全部会话", "All conversations")}</option>
            {(snapshot?.threads ?? []).map((thread) => (
              <option key={thread.id} value={thread.id}>
                {thread.pending_permissions > 0 ? "● " : ""}
                {thread.title}
              </option>
            ))}
          </Select>
          {focusedThread && (
            <button
              type="button"
              className={cx("studio-top-icon")}
              aria-label={t("进入会话", "Open thread")}
              title={t("进入会话", "Open thread")}
              onClick={() => onOpenThread(workspaceID, focusedThread.id)}
            >
              <Icon name="open" size={15} />
            </button>
          )}
        </div>
        <div className={cx("studio-topbar-right")}>
          <span className={cx("studio-live-pill")} role="status">
            <span
              className={cx(`studio-live-dot ${visiblePermissions.length > 0 || error ? "warn" : ""}`)}
              aria-hidden="true"
            />
            {pill}
          </span>
          <button
            type="button"
            className={cx("studio-top-icon")}
            aria-label={t("刷新研发室状态", "Refresh studio")}
            title={t("刷新研发室状态", "Refresh studio")}
            onClick={() => void load(false)}
            disabled={loading}
          >
            <Icon name="refresh" size={14} />
          </button>
          <button
            type="button"
            className={cx("studio-top-icon")}
            aria-label={panelOpen ? t("收起研发室浮窗", "Hide studio overlay") : t("展开研发室浮窗", "Show studio overlay")}
            aria-expanded={panelOpen}
            onClick={() => setPanelOpen((open) => !open)}
          >
            <Icon name="panel" size={14} />
          </button>
        </div>
      </header>

      {error && (
        <p role="alert" className={cx("studio-error-banner")}>
          {error}
        </p>
      )}

      <div className={cx("studio-viewport")} data-testid="studio-viewport" data-panel-open={panelOpen}>
        <div
          className={cx("studio-scene-scroll")}
          data-testid="studio-scene-scroll"
          role="region"
          aria-label={t("研发室场景", "Studio room")}
          tabIndex={0}
        >
          <StudioScene
            cats={placed}
            relays={visibleRelays}
            artifacts={visibleArtifacts}
            permission={visiblePermissions[0]}
            permissionCount={visiblePermissions.length}
            selectedAgentID={selectedAgentID}
            selectedArtifactID={selectedArtifact?.id ?? ""}
            reducedMotion={reducedMotion}
            emotes={emotes}
            reacting={reacting}
            onSelectAgent={pick}
            onSelectArtifact={(artifact) => {
              setSelectedAgentID("");
              setSelectedArtifact(artifact);
              setPanelOpen(true);
            }}
            onReviewPermission={setApproval}
            onFloor={onFloor}
          />
        </div>

        {panelOpen && (
          <aside className={cx("studio-overlay-rail")} data-testid="studio-overlay-rail">
            <section className={cx("studio-card")}>
              <div className={cx("studio-card-head")}>
                <h3>{t("研发室成员", "Crew")}</h3>
                <span className={cx("studio-card-sub")}>{visibleAgents.length}</span>
              </div>
              {/* SECTION: crew-list (skeleton; replaced with avatar rows) */}
              <div className={cx("studio-crew-list")}>
                {visibleAgents.map((agent) => (
                  <button
                    key={agent.agent_id}
                    type="button"
                    className={cx("studio-crew-row")}
                    aria-pressed={selectedAgent?.agent_id === agent.agent_id}
                    onClick={() => pick(agent.agent_id)}
                  >
                    <Avatar
                      name={agent.display_name}
                      avatar={agent.avatar || resolveStudioCat(agent.avatar, agent.handle || agent.display_name).value}
                      executor={agent.executor}
                    />
                    <span className={cx("studio-crew-info")}>
                      <span className={cx("studio-crew-name")}>{agent.display_name}</span>
                      <span className={cx(`studio-state-tag ${stateKind(agent.state)}`)}>
                        {stateLabel(agent.state)}
                      </span>
                    </span>
                  </button>
                ))}
              </div>
            </section>
            {selectedArtifact ? (
              <section className={cx("studio-card")}>
                <div className={cx("studio-card-head")}>
                  <h3>{t("宝石交付物", "Deliverable Gem")}</h3>
                  <button
                    type="button"
                    className={cx("studio-top-icon")}
                    aria-label={t("关闭宝石详情", "Close deliverable")}
                    onClick={() => setSelectedArtifact(null)}
                  >
                    <Icon name="close" size={13} />
                  </button>
                </div>
                <div className={cx("studio-inspector-meta")}>
                  <strong>{selectedArtifact.title}</strong>
                  <span className={cx("studio-card-sub")}>
                    v{selectedArtifact.version} · {selectedArtifact.thread_title}
                  </span>
                  <button
                    type="button"
                    className={cx("studio-open-btn")}
                    onClick={() =>
                      onOpenThread(workspaceID, selectedArtifact.thread_id)
                    }
                  >
                    {t("在会话中查看", "View in thread")}
                  </button>
                </div>
              </section>
            ) : selectedAgent ? (
              <section className={cx("studio-card")}>
                <div className={cx("studio-card-head")}>
                  <h3>{selectedAgent.display_name}</h3>
                  <span
                    className={cx(
                      `studio-state-tag ${stateKind(selectedAgent.state)}`,
                    )}
                  >
                    {stateLabel(selectedAgent.state)}
                  </span>
                </div>
                <div className={cx("studio-inspector-meta")}>
                  <span className={cx("studio-inspector-sub")}>
                    @{selectedAgent.handle} · {selectedAgent.executor || "default"} /{" "}
                    {selectedAgent.model}
                  </span>
                  <p className={cx("studio-inspector-summary")}>
                    {selectedAgent.activity_summary ||
                      poseDescription(
                        placed.find(
                          (item) => item.agent.agent_id === selectedAgent.agent_id,
                        )?.motion.pose,
                      )}
                  </p>
                  {stateKind(selectedAgent.state) === "idle" && (
                    <div className={cx("studio-action-chips")}>
                      {(
                        [
                          { kind: "pet", label: t("摸一摸", "Pet") },
                          { kind: "work", label: t("去办公", "Desk"), emote: "</>" },
                          { kind: "board", label: t("画白板", "Board"), emote: "💡" },
                          { kind: "play", label: t("玩毛线", "Play"), emote: "🧶 ♪" },
                          { kind: "nap", label: t("打个盹", "Nap"), emote: "Zzz..." },
                        ] as const
                      ).map((action) => (
                        <button
                          key={action.kind}
                          type="button"
                          className={cx("studio-action-chip")}
                          onClick={() => {
                            if (action.kind === "pet") {
                              react(selectedAgent.agent_id);
                              return;
                            }
                            sendTo(selectedAgent.agent_id, QUICK_SPOTS[action.kind]);
                            react(selectedAgent.agent_id, action.emote);
                          }}
                        >
                          {action.label}
                        </button>
                      ))}
                    </div>
                  )}
                  {selectedAgent.thread_id && (
                    <button
                      type="button"
                      className={cx("studio-open-btn")}
                      onClick={() =>
                        onOpenThread(workspaceID, selectedAgent.thread_id!)
                      }
                    >
                      <Icon name="open" size={13} />
                      {t("打开实时对话流", "Open live thread")}
                    </button>
                  )}
                </div>
              </section>
            ) : null}
          </aside>
        )}

        <div className={cx("studio-dock")}>
          <StudioCommand
            key={workspaceID}
            agents={visibleAgents}
            sending={sending}
            error={commandError}
            onDismissError={() => setCommandError("")}
            onSend={handleCommand}
          />
        </div>
      </div>

      {approval && (
        <Modal title={t("门禁审批", "Permission request")} close={() => setApproval(undefined)}>
          <PermissionDecision
            prompt={permissionPrompt(approval.request)}
            request={approval.request}
            onDecide={async (decision, grantScope) => {
              await submitPermissionDecision(approval.request, decision, grantScope);
              setSnapshot(
                (current) =>
                  current && {
                    ...current,
                    pending_permissions: current.pending_permissions.filter(
                      (item) => item.request.id !== approval.request.id,
                    ),
                  },
              );
              setApproval(undefined);
              void load(true, true);
            }}
          />
        </Modal>
      )}
    </div>
  );
}
