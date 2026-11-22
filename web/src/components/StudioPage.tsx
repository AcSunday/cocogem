import { useCallback, useEffect, useMemo, useState } from "react";
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

type Point = { x: number; y: number };

const STATION_SLOTS: Record<string, Point[]> = {
  whiteboard: [
    { x: 185, y: 205 },
    { x: 235, y: 222 },
  ],
  research: [
    { x: 358, y: 176 },
    { x: 408, y: 192 },
  ],
  coding: [
    { x: 446, y: 245 },
    { x: 382, y: 266 },
    { x: 505, y: 268 },
  ],
  review: [
    { x: 556, y: 234 },
    { x: 516, y: 214 },
  ],
  gate: [
    { x: 325, y: 318 },
    { x: 378, y: 330 },
  ],
  idle: [
    { x: 182, y: 215 },
    { x: 355, y: 185 },
    { x: 552, y: 242 },
    { x: 324, y: 318 },
    { x: 445, y: 248 },
    { x: 245, y: 268 },
    { x: 486, y: 304 },
    { x: 265, y: 186 },
    { x: 422, y: 162 },
    { x: 598, y: 280 },
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
      return 14;
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

  // Keep the Studio live with lightweight periodic refreshes while mounted.
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
      const basePt = slots[slotIndex % slots.length] ?? STATION_SLOTS.idle[idx % STATION_SLOTS.idle.length];
      const overflow = Math.floor(slotIndex / slots.length);
      const pos: Point = {
        x: basePt.x + overflow * 28,
        y: basePt.y + overflow * 12,
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
          <svg
            viewBox="0 0 760 440"
            className={cx("studio-scene-svg")}
            role="img"
            aria-label={t(
              "Cocogem 像素猫猫研发室实时场景",
              "Cocogem Pixel Cat R&D Studio live scene",
            )}
          >
            <defs>
              <radialGradient id="lamp-glow-1" cx="50%" cy="50%" r="50%">
                <stop offset="0%" stopColor="#fbbf24" stopOpacity="0.34" />
                <stop offset="100%" stopColor="#fbbf24" stopOpacity="0" />
              </radialGradient>
              <radialGradient id="lamp-glow-2" cx="50%" cy="50%" r="50%">
                <stop offset="0%" stopColor="#38bdf8" stopOpacity="0.22" />
                <stop offset="100%" stopColor="#38bdf8" stopOpacity="0" />
              </radialGradient>
              <marker
                id="relay-arrowhead"
                markerWidth="8"
                markerHeight="8"
                refX="6"
                refY="4"
                orient="auto"
              >
                <path d="M0,1 L7,4 L0,7 Z" fill="#2dd4bf" />
              </marker>
            </defs>

            {/* Isometric room back walls */}
            <polygon
              points="380,44 96,178 96,294 380,162"
              fill="#2a2229"
              stroke="#141118"
              strokeWidth="2"
            />
            <polygon
              points="380,44 664,178 664,294 380,162"
              fill="#221c24"
              stroke="#141118"
              strokeWidth="2"
            />
            {/* Warm wood wainscot trim */}
            <polygon
              points="380,126 96,260 96,294 380,162"
              fill="#4b3226"
              opacity="0.72"
            />
            <polygon
              points="380,126 664,260 664,294 380,162"
              fill="#3d271e"
              opacity="0.78"
            />

            {/* Isometric oak wood floor */}
            <polygon
              points="380,158 664,292 380,418 96,292"
              fill="#6e472d"
              stroke="#1c1412"
              strokeWidth="2"
            />
            {/* Floorboard plank seams */}
            {[0.16, 0.32, 0.48, 0.64, 0.8].map((ratio) => {
              const lx = 380 - 284 * ratio;
              const ly = 158 + 134 * ratio;
              const rx = lx + 284;
              const ry = ly + 126;
              return (
                <line
                  key={ratio}
                  x1={lx}
                  y1={ly}
                  x2={rx}
                  y2={ry}
                  stroke="#4e301d"
                  strokeWidth="1.5"
                  opacity="0.65"
                />
              );
            })}

            {/* Starry night window on right back wall */}
            <g shapeRendering="crispEdges">
              <polygon
                points="424,84 484,112 484,172 424,144"
                fill="#0d1630"
                stroke="#4a352a"
                strokeWidth="3"
              />
              <rect x="440" y="106" width="3" height="3" fill="#fde047" />
              <rect x="462" y="122" width="2" height="2" fill="#e2e8f0" />
              <rect x="450" y="136" width="2" height="2" fill="#93c5fd" />
              <rect x="470" y="144" width="3" height="3" fill="#fde68a" />
              <line x1="454" y1="98" x2="454" y2="158" stroke="#4a352a" strokeWidth="2" />
              <line x1="424" y1="114" x2="484" y2="142" stroke="#4a352a" strokeWidth="2" />
            </g>

            {/* Glowing glass whiteboard on left wall (Coordinator / Planning station) */}
            <g shapeRendering="crispEdges">
              <polygon
                points="142,158 248,108 248,176 142,226"
                fill="#dbeafe"
                stroke="#64748b"
                strokeWidth="3"
              />
              <polygon
                points="148,162 242,118 242,172 148,216"
                fill="#eff6ff"
              />
              {/* Pixel sticky notes & flowchart on whiteboard */}
              <rect x="160" y="168" width="14" height="12" fill="#fbbf24" />
              <rect x="184" y="152" width="16" height="12" fill="#60a5fa" />
              <rect x="210" y="138" width="16" height="12" fill="#34d399" />
              <rect x="176" y="182" width="18" height="10" fill="#f472b6" />
              <line x1="174" y1="172" x2="184" y2="160" stroke="#475569" strokeWidth="1.5" />
              <line x1="200" y1="156" x2="210" y2="146" stroke="#475569" strokeWidth="1.5" />
            </g>

            {/* Bookshelf near Research desk */}
            <g shapeRendering="crispEdges">
              <polygon
                points="284,96 346,68 346,142 284,170"
                fill="#5c3924"
                stroke="#231610"
                strokeWidth="2"
              />
              <rect x="294" y="100" width="8" height="16" fill="#3b82f6" />
              <rect x="304" y="96" width="7" height="16" fill="#e06d53" />
              <rect x="313" y="92" width="8" height="16" fill="#2dd4bf" />
              <rect x="323" y="88" width="8" height="16" fill="#fbbf24" />
              <rect x="294" y="126" width="9" height="16" fill="#a78bfa" />
              <rect x="306" y="122" width="8" height="16" fill="#34d399" />
              <rect x="317" y="116" width="10" height="16" fill="#f472b6" />
            </g>

            {/* Glass Gem Showcase Shelf on back right wall (Workspace Artifacts!) */}
            <g shapeRendering="crispEdges">
              <polygon
                points="512,118 624,172 624,218 512,164"
                fill="rgba(148, 163, 184, 0.14)"
                stroke="#64748b"
                strokeWidth="2"
              />
              <line x1="512" y1="164" x2="624" y2="218" stroke="#94a3b8" strokeWidth="3" />
              {(visibleArtifacts.length > 0
                ? visibleArtifacts.slice(0, 6)
                : [null, null, null]
              ).map((art, i) => {
                const gx = 528 + i * 16;
                const gy = 148 + i * 8;
                const color = GEM_COLORS[i % GEM_COLORS.length];
                const isPlaceholder = !art;
                return (
                  <g
                    key={art ? `${art.id}-${art.version}` : `empty-gem-${i}`}
                    className={art ? cx("studio-gem-btn") : undefined}
                    onClick={() => {
                      if (art) setSelectedArtifact(art);
                    }}
                  >
                    <title>
                      {art
                        ? `${art.title} (v${art.version})`
                        : t("交付产物后将点亮宝石", "Complete deliverables to light up gems")}
                    </title>
                    <polygon
                      points={`${gx},${gy - 8} ${gx + 7},${gy - 2} ${gx},${gy + 6} ${gx - 7},${gy - 2}`}
                      fill={isPlaceholder ? "#334155" : color}
                      opacity={isPlaceholder ? 0.35 : 0.95}
                      stroke="#ffffff"
                      strokeOpacity={isPlaceholder ? 0.12 : 0.55}
                      strokeWidth="1"
                    />
                  </g>
                );
              })}
            </g>

            {/* Research Dual-Monitor Desk (top center) */}
            <g shapeRendering="crispEdges">
              <polygon
                points="324,186 400,152 444,174 368,208"
                fill="#8b5a38"
                stroke="#261810"
                strokeWidth="2"
              />
              <polygon
                points="368,208 444,174 444,186 368,220"
                fill="#5c3822"
              />
              {/* Dual monitors */}
              <polygon
                points="346,162 374,149 374,170 346,183"
                fill="#0f172a"
                stroke="#475569"
                strokeWidth="2"
              />
              <rect x="352" y="160" width="14" height="3" fill="#38bdf8" />
              <rect x="352" y="166" width="10" height="3" fill="#2dd4bf" />
              <polygon
                points="380,148 408,136 408,157 380,169"
                fill="#0f172a"
                stroke="#475569"
                strokeWidth="2"
              />
              <rect x="386" y="146" width="14" height="3" fill="#a78bfa" />
              <rect x="386" y="152" width="12" height="3" fill="#34d399" />
            </g>

            {/* Coding & Review Workstations (right center) */}
            <g shapeRendering="crispEdges">
              {/* Coding desk */}
              <polygon
                points="418,248 494,214 538,236 462,270"
                fill="#8b5a38"
                stroke="#261810"
                strokeWidth="2"
              />
              <polygon
                points="462,270 538,236 538,248 462,282"
                fill="#5c3822"
              />
              <polygon
                points="446,222 480,206 480,230 446,246"
                fill="#0f172a"
                stroke="#475569"
                strokeWidth="2"
              />
              <rect x="454" y="220" width="16" height="3" fill="#34d399" />
              <rect x="454" y="226" width="12" height="3" fill="#4f8bff" />

              {/* Review desk */}
              <polygon
                points="522,256 598,222 638,242 562,276"
                fill="#8b5a38"
                stroke="#261810"
                strokeWidth="2"
              />
              <polygon
                points="562,276 638,242 638,254 562,288"
                fill="#5c3822"
              />
              <polygon
                points="550,230 584,214 584,238 550,254"
                fill="#0f172a"
                stroke="#475569"
                strokeWidth="2"
              />
              <rect x="558" y="228" width="14" height="3" fill="#4ade80" />
              <rect x="558" y="234" width="10" height="3" fill="#f87171" />
            </g>

            {/* Foreground Wooden Security Gatekeeper Desk */}
            <g shapeRendering="crispEdges">
              <polygon
                points="266,326 352,288 394,308 308,346"
                fill="#7a4c2e"
                stroke="#24160f"
                strokeWidth="2"
              />
              <polygon
                points="308,346 394,308 394,332 308,370"
                fill="#4e2f1c"
              />
              {/* Wooden gate rails */}
              <line x1="254" y1="306" x2="296" y2="326" stroke="#9a643d" strokeWidth="4" />
              <line x1="254" y1="322" x2="296" y2="342" stroke="#9a643d" strokeWidth="3" />
              <line x1="262" y1="306" x2="262" y2="338" stroke="#6b4227" strokeWidth="3" />
              <line x1="286" y1="318" x2="286" y2="350" stroke="#6b4227" strokeWidth="3" />
            </g>

            {/* Cozy potted pixel plants */}
            {[
              { x: 136, y: 268 },
              { x: 256, y: 162 },
              { x: 616, y: 288 },
            ].map((plant, idx) => (
              <g key={idx} shapeRendering="crispEdges">
                <rect x={plant.x - 7} y={plant.y} width="14" height="10" fill="#b45309" />
                <rect x={plant.x - 9} y={plant.y - 2} width="18" height="3" fill="#d97706" />
                <rect x={plant.x - 10} y={plant.y - 12} width="8" height="10" fill="#15803d" />
                <rect x={plant.x - 3} y={plant.y - 18} width="8" height="16" fill="#22c55e" />
                <rect x={plant.x + 3} y={plant.y - 11} width="8" height="9" fill="#16a34a" />
              </g>
            ))}

            {/* Warm ambient desk lamp halos */}
            <circle cx="384" cy="180" r="64" fill="url(#lamp-glow-1)" />
            <circle cx="534" cy="240" r="72" fill="url(#lamp-glow-1)" />
            <circle cx="200" cy="180" r="68" fill="url(#lamp-glow-2)" />

            {/* Live glowing teal A2A Relay Arrows between cat stations */}
            {visibleRelays.map((relay) => {
              const from = positionByAgentID.get(relay.from_agent_id);
              const to = positionByAgentID.get(relay.to_agent_id);
              if (!from || !to) return null;
              const mx = (from.x + to.x) / 2;
              const my = Math.min(from.y, to.y) - 36;
              return (
                <path
                  key={`${relay.run_id}-${relay.step_id}`}
                  d={`M ${from.x} ${from.y - 18} Q ${mx} ${my} ${to.x} ${to.y - 18}`}
                  className={cx("studio-relay-path")}
                  markerEnd="url(#relay-arrowhead)"
                />
              );
            })}

            {/* Pixel Cat Members */}
            {placedCats.map(({ agent, cat, pos }) => {
              const isSelected = selectedAgent?.agent_id === agent.agent_id;
              const kind = stateKind(agent.state);
              const pose = poseForAgent(agent.state);
              const dotColor =
                kind === "warn"
                  ? "#fbbf24"
                  : kind === "error"
                    ? "#f87171"
                    : kind === "active"
                      ? "#2dd4bf"
                      : "#64748b";
              return (
                <g
                  key={agent.agent_id}
                  transform={`translate(${pos.x - 28}, ${pos.y - 46})`}
                  className={cx(
                    `studio-cat-group ${isSelected ? "studio-cat-selected" : ""}`,
                  )}
                  onClick={() => {
                    setSelectedArtifact(null);
                    setSelectedAgentID(agent.agent_id);
                  }}
                >
                  <title>
                    {`${agent.display_name} (${t(cat.zhName, cat.enName)}) · ${stateLabel(agent.state)}`}
                  </title>
                  {/* Selection ring on floor */}
                  {isSelected && (
                    <ellipse
                      cx="28"
                      cy="50"
                      rx="22"
                      ry="9"
                      fill="none"
                      stroke="#2dd4bf"
                      strokeWidth="2"
                      opacity="0.85"
                    />
                  )}
                  <g
                    className={cx(
                      kind === "active" ? "studio-cat-work" : "studio-cat-bob",
                    )}
                  >
                    <CatCharacter preset={cat} pose={pose} size={56} />
                  </g>
                  {/* Floating name pill below cat */}
                  <g transform="translate(28, 60)">
                    <rect
                      x="-38"
                      y="-9"
                      width="76"
                      height="17"
                      rx="8.5"
                      fill="rgba(12, 14, 18, 0.88)"
                      stroke={
                        isSelected
                          ? "#2dd4bf"
                          : kind === "warn"
                            ? "#fbbf24"
                            : "rgba(255,255,255,0.14)"
                      }
                      strokeWidth="1"
                    />
                    <circle cx="-28" cy="-0.5" r="3" fill={dotColor} />
                    <text
                      x="3"
                      y="3"
                      textAnchor="middle"
                      fill="#f0f2f6"
                      fontSize="9.5"
                      fontWeight="600"
                    >
                      {agent.display_name.slice(0, 8)}
                    </text>
                  </g>
                </g>
              );
            })}
          </svg>

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

        {/* Right Floating Crew & Inspector Column */}
        <aside className={cx("studio-panel")}>
          <section className={cx("studio-card")}>
            <div className={cx("studio-card-head")}>
              <h3>
                {t("研发室成员", "Studio Crew")} ({visibleAgents.length})
              </h3>
              <span className={cx("muted")}>
                {snapshot?.workspace_name}
              </span>
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
                    <span className={cx("studio-gem-title")}>
                      {art.title}
                    </span>
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
