import { useCallback, useEffect, useMemo, useState } from "react";
import roomBg from "../assets/studio/room-bg.webp";
import {
  decidePermissionRequest,
  workspaceStudio,
  type StudioAgentActivity,
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
import { CatCharacter, type CatSpritePose } from "./CatSprite";
import { Icon } from "./Icon";

const cx = scoped(base, controls, studio);

/** Percentage coordinates calibrated to `room-bg.webp` (16:9 isometric studio). */
type Point = { x: number; y: number };

const STATION_SLOTS: Record<string, Point[]> = {
  // Standing in front of the illuminated sticky-note whiteboard
  whiteboard: [
    { x: 29, y: 54 },
    { x: 35, y: 57 },
  ],
  // Seated / working at the top-center dual-monitor research desk
  research: [
    { x: 51.5, y: 40 },
    { x: 44.5, y: 45 },
  ],
  // Working at the lower-right oak coding workstation
  coding: [
    { x: 65, y: 63.5 },
    { x: 56, y: 56 },
    { x: 49, y: 53 },
  ],
  // Reviewing diff at the upper-right developer workstation
  review: [
    { x: 74, y: 56.5 },
    { x: 69, y: 68 },
  ],
  // Foreground wooden security gatekeeper reception desk
  gate: [
    { x: 47.5, y: 74 },
    { x: 40, y: 68 },
  ],
  // Naturally spread across the studio stations so idle cats inhabit the room
  idle: [
    { x: 29.5, y: 55 },
    { x: 51.5, y: 40 },
    { x: 65, y: 64 },
    { x: 47.5, y: 74 },
    { x: 74, y: 56.5 },
    { x: 42, y: 50 },
    { x: 56, y: 57 },
    { x: 38, y: 63 },
    { x: 59, y: 72 },
    { x: 46, y: 44 },
  ],
};

const GEM_COLORS = ["#4f8bff", "#2dd4bf", "#e06d53", "#fbbf24", "#a78bfa", "#34d399"];

function stateLabel(state: string): string {
  switch (state) {
    case "planning":
      return t("规划中", "Planning");
    case "researching":
      return t("研究中", "Researching");
    case "coding":
      return t("开发中", "Coding");
    case "reviewing":
      return t("复核中", "Reviewing");
    case "waiting_permission":
      return t("待审批", "Approval");
    case "failed":
      return t("需处理", "Needs attention");
    case "completed":
      return t("已交付", "Delivered");
    default:
      return t("待机", "Idle");
  }
}

function stateKind(state: string): "active" | "warn" | "error" | "idle" {
  if (state === "waiting_permission") return "warn";
  if (state === "failed") return "error";
  if (
    state === "planning" ||
    state === "researching" ||
    state === "coding" ||
    state === "reviewing"
  ) {
    return "active";
  }
  return "idle";
}

function poseForAgent(state: string): CatSpritePose {
  switch (state) {
    case "planning":
      return "planning";
    case "researching":
      return "researching";
    case "coding":
      return "coding";
    case "reviewing":
      return "reviewing";
    case "waiting_permission":
      return "gate";
    case "failed":
      return "failed";
    default:
      return "idle";
  }
}

function activityProgress(state: string): number {
  switch (state) {
    case "planning":
      return 38;
    case "researching":
      return 55;
    case "coding":
      return 74;
    case "reviewing":
      return 88;
    case "waiting_permission":
      return 64;
    case "completed":
      return 100;
    case "failed":
      return 45;
    default:
      return 16;
  }
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
  useLanguage();
  const [snapshot, setSnapshot] = useState<StudioSnapshot | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [focusedThreadID, setFocusedThreadID] = useState("");
  const [selectedAgentID, setSelectedAgentID] = useState("");
  const [selectedArtifact, setSelectedArtifact] = useState<StudioArtifact | null>(
    null,
  );
  const [decidingID, setDecidingID] = useState("");
  const [command, setCommand] = useState("");
  const [sending, setSending] = useState(false);

  const load = useCallback(
    async (silent = false) => {
      if (!workspaceID) return;
      if (!silent) setLoading(true);
      try {
        const next = await workspaceStudio(workspaceID);
        setSnapshot(next);
        setError("");
      } catch (failure) {
        setError(errorText(failure));
      } finally {
        if (!silent) setLoading(false);
      }
    },
    [workspaceID],
  );

  useEffect(() => {
    setFocusedThreadID("");
    setSelectedAgentID("");
    setSelectedArtifact(null);
    void load(false);
  }, [load]);

  useEffect(() => {
    if (!workspaceID) return;
    const timer = window.setInterval(() => {
      void load(true);
    }, 3000);
    return () => window.clearInterval(timer);
  }, [workspaceID, load]);

  const visibleAgents = useMemo(() => {
    const list = (snapshot?.agents ?? []).filter((ag) => ag.enabled);
    if (!focusedThreadID) return list;
    return list.map((ag) =>
      ag.thread_id === focusedThreadID
        ? ag
        : {
            ...ag,
            state: "idle",
            station: "idle",
            activity_summary: "",
          },
    );
  }, [snapshot, focusedThreadID]);

  const placedCats = useMemo(() => {
    const usedByStation: Record<string, number> = {};
    return visibleAgents.map((agent, idx) => {
      const key =
        agent.station && STATION_SLOTS[agent.station] ? agent.station : "idle";
      const slots = STATION_SLOTS[key];
      const slotIndex = usedByStation[key] ?? 0;
      usedByStation[key] = slotIndex + 1;
      const basePt =
        slots[slotIndex % slots.length] ??
        STATION_SLOTS.idle[idx % STATION_SLOTS.idle.length];
      const overflow = Math.floor(slotIndex / slots.length);
      const pos: Point = {
        x: Math.min(82, Math.max(18, basePt.x + overflow * 5)),
        y: Math.min(80, Math.max(30, basePt.y + overflow * 4)),
      };
      return {
        agent,
        cat: resolveStudioCat(agent.avatar, agent.handle || agent.display_name),
        pos,
      };
    });
  }, [visibleAgents]);

  const positionByAgentID = useMemo(() => {
    const map = new Map<string, Point>();
    for (const item of placedCats) {
      map.set(item.agent.agent_id, item.pos);
    }
    return map;
  }, [placedCats]);

  const visibleRelays = useMemo(() => {
    const list = snapshot?.relays ?? [];
    if (!focusedThreadID) return list;
    return list.filter((item) => item.thread_id === focusedThreadID);
  }, [snapshot, focusedThreadID]);

  const visiblePermissions = useMemo(() => {
    const list = snapshot?.pending_permissions ?? [];
    if (!focusedThreadID) return list;
    return list.filter((item) => item.request.thread_id === focusedThreadID);
  }, [snapshot, focusedThreadID]);

  const visibleArtifacts = useMemo(() => {
    const list = snapshot?.artifacts ?? [];
    if (!focusedThreadID) return list;
    return list.filter((item) => item.thread_id === focusedThreadID);
  }, [snapshot, focusedThreadID]);

  const activePermission = visiblePermissions[0];
  const selectedAgent: StudioAgentActivity | undefined =
    visibleAgents.find((ag) => ag.agent_id === selectedAgentID) ??
    visibleAgents[0];

  const handlePermission = async (
    decision: "allow_once" | "allow_conversation" | "deny",
  ) => {
    if (!activePermission || decidingID) return;
    setDecidingID(activePermission.request.id);
    try {
      await decidePermissionRequest(activePermission.request, decision);
      await load(true);
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setDecidingID("");
    }
  };

  const handleCommand = async () => {
    const text = command.trim();
    if (!text || sending) return;
    setSending(true);
    setError("");
    try {
      const targetThread = await onDispatch(
        text,
        focusedThreadID || undefined,
      );
      setCommand("");
      if (targetThread) {
        setFocusedThreadID(targetThread);
      }
      await load(true);
    } catch (failure) {
      setError(errorText(failure));
    } finally {
      setSending(false);
    }
  };

  const activeCount = visibleAgents.filter(
    (ag) => stateKind(ag.state) === "active",
  ).length;
  const focusedThread = snapshot?.threads.find(
    (item) => item.id === focusedThreadID,
  );

  if (!workspaceID) {
    return (
      <div className={cx("studio-page")}>
        <div className={cx("studio-card")}>
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

  return (
    <div className={cx("studio-page")} data-testid="studio-page">
      <div className={cx("studio-toolbar")}>
        <div className={cx("studio-toolbar-left")}>
          {workspaces.length > 1 && (
            <select
              className={cx("studio-select")}
              aria-label={t("切换研发室工作空间", "Switch studio workspace")}
              value={workspaceID}
              onChange={(event) => onWorkspace(event.target.value)}
            >
              {workspaces.map((ws) => (
                <option key={ws.id} value={ws.id}>
                  {ws.name}
                </option>
              ))}
            </select>
          )}
          <select
            className={cx("studio-select")}
            aria-label={t("聚焦会话", "Focus conversation")}
            value={focusedThreadID}
            onChange={(event) => setFocusedThreadID(event.target.value)}
          >
            <option value="">
              {t(
                `全部活跃会话（总览 · ${snapshot?.threads.length ?? 0}）`,
                `All active threads (Overview · ${snapshot?.threads.length ?? 0})`,
              )}
            </option>
            {(snapshot?.threads ?? []).map((thread) => (
              <option key={thread.id} value={thread.id}>
                {thread.pending_permissions > 0 ? "● " : ""}
                {thread.title}
              </option>
            ))}
          </select>
          {focusedThread && (
            <button
              type="button"
              className={cx("ghost small")}
              onClick={() => onOpenThread(workspaceID, focusedThread.id)}
            >
              <Icon name="open" size={13} />
              {t("进入会话", "Open thread")}
            </button>
          )}
        </div>

        <div className={cx("studio-toolbar-left")}>
          <span className={cx("studio-live-pill")} role="status">
            <span
              className={cx(
                `studio-live-dot ${visiblePermissions.length > 0 ? "warn" : ""}`,
              )}
              aria-hidden="true"
            />
            {visiblePermissions.length > 0
              ? t(
                  `${visiblePermissions.length} 项待审批`,
                  `${visiblePermissions.length} pending approval`,
                )
              : activeCount > 0
                ? t(
                    `${activeCount} 只猫猫正在协作`,
                    `${activeCount} cat${activeCount === 1 ? "" : "s"} active`,
                  )
                : t("研发室待机中", "Studio live")}
          </span>
          <button
            type="button"
            className={cx("icon-button small")}
            aria-label={t("刷新研发室状态", "Refresh studio")}
            title={t("刷新研发室状态", "Refresh studio")}
            onClick={() => void load(false)}
            disabled={loading}
          >
            <Icon name="refresh" size={15} />
          </button>
        </div>
      </div>

      {error && (
        <p role="alert" className={cx("error")}>
          {error}
        </p>
      )}

      <div className={cx("studio-stage-shell")}>
        <div className={cx("studio-viewport")}>
          <div
            className={cx("studio-canvas-frame")}
            role="img"
            aria-label={t(
              "Cocogem 像素猫猫研发室实时场景",
              "Cocogem Pixel Cat R&D Studio live scene",
            )}
          >
            <img
              src={roomBg}
              alt=""
              draggable={false}
              className={cx("studio-room-bg")}
            />

            {/* Animated glowing teal A2A Relay Arrows */}
            {visibleRelays.length > 0 && (
              <svg
                viewBox="0 0 100 100"
                preserveAspectRatio="none"
                className={cx("studio-relay-svg")}
                aria-hidden="true"
              >
                <defs>
                  <marker
                    id="studio-relay-arrow"
                    markerWidth="4"
                    markerHeight="4"
                    refX="3"
                    refY="2"
                    orient="auto"
                  >
                    <path d="M0,0.5 L3.5,2 L0,3.5 Z" fill="#2dd4bf" />
                  </marker>
                </defs>
                {visibleRelays.map((relay) => {
                  const from = positionByAgentID.get(relay.from_agent_id);
                  const to = positionByAgentID.get(relay.to_agent_id);
                  if (!from || !to) return null;
                  const mx = (from.x + to.x) / 2;
                  const my = Math.min(from.y, to.y) - 9;
                  return (
                    <path
                      key={`${relay.run_id}-${relay.step_id}`}
                      d={`M ${from.x} ${from.y - 3} Q ${mx} ${my} ${to.x} ${to.y - 3}`}
                      className={cx("studio-relay-path")}
                      markerEnd="url(#studio-relay-arrow)"
                    />
                  );
                })}
              </svg>
            )}

            {/* Interactive glowing gems aligned inside the right-wall glass display cabinet */}
            <div className={cx("studio-gem-shelf")}>
              {visibleArtifacts.slice(0, 6).map((art, i) => {
                const color = GEM_COLORS[i % GEM_COLORS.length];
                return (
                  <button
                    key={`${art.id}-${art.version}`}
                    type="button"
                    className={cx("studio-gem-slot")}
                    title={`${art.title} (v${art.version})`}
                    aria-label={`${art.title} v${art.version}`}
                    onClick={() => setSelectedArtifact(art)}
                  >
                    <svg width="14" height="14" viewBox="0 0 16 16" aria-hidden="true">
                      <polygon
                        points="8,1 14,6 8,15 2,6"
                        fill={color}
                        stroke="#ffffff"
                        strokeOpacity="0.6"
                        strokeWidth="1"
                      />
                      <polygon points="8,1 11,6 8,15 5,6" fill="#ffffff" opacity="0.28" />
                    </svg>
                  </button>
                );
              })}
            </div>

            {/* Live High-Craft Pixel Cat Characters */}
            {placedCats.map(({ agent, cat, pos }) => {
              const isSelected = selectedAgent?.agent_id === agent.agent_id;
              const kind = stateKind(agent.state);
              const pose = poseForAgent(agent.state);
              return (
                <button
                  key={agent.agent_id}
                  type="button"
                  className={cx("studio-cat-actor")}
                  style={{ left: `${pos.x}%`, top: `${pos.y}%` }}
                  aria-pressed={isSelected}
                  title={`${agent.display_name} (${t(cat.zhName, cat.enName)}) · ${stateLabel(agent.state)}`}
                  onClick={() => {
                    setSelectedArtifact(null);
                    setSelectedAgentID(agent.agent_id);
                  }}
                >
                  {kind !== "idle" && (
                    <span
                      className={cx(
                        `studio-cat-bubble ${kind === "warn" ? "warn" : ""}`,
                      )}
                    >
                      {stateLabel(agent.state)}
                    </span>
                  )}
                  {isSelected && (
                    <span className={cx("studio-cat-ring")} aria-hidden="true" />
                  )}
                  <span
                    className={cx(
                      kind === "active" ? "studio-cat-work" : "studio-cat-bob",
                    )}
                  >
                    <CatCharacter preset={cat} pose={pose} size={76} />
                  </span>
                  <span className={cx("studio-cat-tag")}>
                    <span className={cx(`studio-cat-dot ${kind}`)} />
                    <span>{agent.display_name}</span>
                  </span>
                </button>
              );
            })}
          </div>

          {/* Inline foreground Gatekeeper permission callout */}
          {activePermission && (
            <div
              className={cx("studio-gate-callout")}
              role="region"
              aria-label={t("研发室待审批请求", "Studio pending approval")}
            >
              <div className={cx("studio-gate-head")}>
                <span className={cx("studio-gate-title")}>
                  <Icon name="shield" size={14} />
                  {t("门禁审批", "Permission")} ·{" "}
                  {activePermission.agent_name || t("成员", "Member")}
                </span>
                <span className={cx("muted")}>
                  {activePermission.thread_title}
                </span>
              </div>
              <div className={cx("studio-gate-cmd")}>
                {activePermission.request.operation_summary ||
                  activePermission.request.reason ||
                  activePermission.request.tool_name}
              </div>
              <div className={cx("studio-gate-actions")}>
                <button
                  type="button"
                  className={cx("studio-gate-btn approve")}
                  disabled={decidingID === activePermission.request.id}
                  onClick={() => void handlePermission("allow_once")}
                >
                  {t("仅本次允许", "Allow once")}
                </button>
                {activePermission.request.risk !== "sensitive_path" && (
                  <button
                    type="button"
                    className={cx("studio-gate-btn")}
                    disabled={decidingID === activePermission.request.id}
                    onClick={() => void handlePermission("allow_conversation")}
                  >
                    {t("本会话允许", "Allow in session")}
                  </button>
                )}
                <button
                  type="button"
                  className={cx("studio-gate-btn deny")}
                  disabled={decidingID === activePermission.request.id}
                  onClick={() => void handlePermission("deny")}
                >
                  {t("拒绝", "Deny")}
                </button>
              </div>
            </div>
          )}

          {/* Bottom floating command bar */}
          <form
            className={cx("studio-command-bar")}
            onSubmit={(event) => {
              event.preventDefault();
              void handleCommand();
            }}
          >
            <input
              type="text"
              value={command}
              onChange={(event) => setCommand(event.target.value)}
              placeholder={
                focusedThread
                  ? t(
                      `在「${focusedThread.title}」里追问或 @指定猫猫…`,
                      `Follow up in "${focusedThread.title}" or @mention…`,
                    )
                  : t(
                      "向猫猫研发室派活，或 @指定成员开启新任务…",
                      "Message Cat Studio or @mention a teammate…",
                    )
              }
              aria-label={t("猫猫研发室指令输入框", "Cat Studio command input")}
              disabled={sending}
            />
            <button
              type="submit"
              className={cx("studio-send")}
              aria-label={t("发送指令", "Send command")}
              disabled={!command.trim() || sending}
            >
              <Icon name="send" size={15} />
            </button>
          </form>
        </div>

        {/* Right Crew & Inspector Column */}
        <aside className={cx("studio-panel")}>
          <section className={cx("studio-card")}>
            <div className={cx("studio-card-head")}>
              <h3>
                {t("研发室成员", "Studio Crew")} ({visibleAgents.length})
              </h3>
              <span className={cx("muted")}>{snapshot?.workspace_name}</span>
            </div>
            <div className={cx("studio-crew-list")}>
              {visibleAgents.map((agent) => {
                const kind = stateKind(agent.state);
                const cat = resolveStudioCat(
                  agent.avatar,
                  agent.handle || agent.display_name,
                );
                return (
                  <button
                    key={agent.agent_id}
                    type="button"
                    className={cx("studio-crew-row")}
                    aria-pressed={selectedAgent?.agent_id === agent.agent_id}
                    onClick={() => {
                      setSelectedArtifact(null);
                      setSelectedAgentID(agent.agent_id);
                    }}
                  >
                    <Avatar
                      name={agent.display_name}
                      avatar={agent.avatar || cat.value}
                      executor={agent.executor}
                      small
                    />
                    <span className={cx("studio-crew-info")}>
                      <span className={cx("studio-crew-top")}>
                        <span className={cx("studio-crew-name")}>
                          {agent.display_name}
                        </span>
                        <span className={cx(`studio-state-tag ${kind}`)}>
                          {stateLabel(agent.state)}
                        </span>
                      </span>
                      <span className={cx("studio-bar")}>
                        <span
                          className={cx(`studio-bar-fill ${kind}`)}
                          style={{ width: `${activityProgress(agent.state)}%` }}
                        />
                      </span>
                    </span>
                  </button>
                );
              })}
            </div>
          </section>

          {selectedArtifact ? (
            <section className={cx("studio-card")}>
              <div className={cx("studio-card-head")}>
                <h3>{t("宝石交付物", "Deliverable Gem")}</h3>
                <button
                  type="button"
                  className={cx("ghost small")}
                  onClick={() => setSelectedArtifact(null)}
                >
                  <Icon name="close" size={13} />
                </button>
              </div>
              <div className={cx("studio-inspector-meta")}>
                <strong>{selectedArtifact.title}</strong>
                <span>
                  v{selectedArtifact.version} · {selectedArtifact.thread_title}
                </span>
                <button
                  type="button"
                  className={cx("primary small")}
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
                <span>
                  @{selectedAgent.handle} ·{" "}
                  {selectedAgent.executor || "default"} / {selectedAgent.model}
                </span>
                {selectedAgent.thread_title && (
                  <span>
                    {t("所属会话：", "Thread: ")}
                    <strong>{selectedAgent.thread_title}</strong>
                  </span>
                )}
                {selectedAgent.activity_summary ? (
                  <p className={cx("studio-inspector-summary")}>
                    {selectedAgent.activity_summary}
                  </p>
                ) : (
                  <p className={cx("muted")}>
                    {t(
                      "当前处于待机状态，随时可以通过底部指令条 @它 派活。",
                      "Currently idle. Mention it from the command bar below to assign work.",
                    )}
                  </p>
                )}
                {selectedAgent.thread_id && (
                  <button
                    type="button"
                    className={cx("small")}
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

          <section className={cx("studio-card")}>
            <div className={cx("studio-card-head")}>
              <h3>
                {t("宝石展柜", "Gem Showcase")} ({visibleArtifacts.length})
              </h3>
            </div>
            {visibleArtifacts.length === 0 ? (
              <p className={cx("muted")}>
                {t(
                  "本空间完成协作报告或交付物后，会自动点亮墙上的宝石。",
                  "Completed deliverables and reports illuminate gems on the studio shelf.",
                )}
              </p>
            ) : (
              <div className={cx("studio-gem-list")}>
                {visibleArtifacts.slice(0, 5).map((art) => (
                  <button
                    key={`${art.id}-${art.version}`}
                    type="button"
                    className={cx("studio-gem-item")}
                    onClick={() => onOpenThread(workspaceID, art.thread_id)}
                  >
                    <span className={cx("studio-gem-title")}>{art.title}</span>
                    <span className={cx("muted")}>v{art.version}</span>
                  </button>
                ))}
              </div>
            )}
          </section>
        </aside>
      </div>
    </div>
  );
}
