import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent,
} from "react";
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
import { CatCharacter, type CatPose } from "./CatSprite";
import { Icon } from "./Icon";

const cx = scoped(base, controls, studio);

type StationSlot = {
  x: number;
  y: number;
  pose: CatPose;
  flip: boolean;
};

/** Calibrated stations for the 1376x768 full-bleed isometric pixel room. */
const ACTIVE_STATIONS: Record<string, StationSlot[]> = {
  whiteboard: [
    { x: 24.5, y: 54.5, pose: "board", flip: false },
    { x: 31, y: 50, pose: "board", flip: false },
  ],
  research: [
    { x: 41.2, y: 63.5, pose: "work", flip: true },
    { x: 68.5, y: 61, pose: "work", flip: false },
  ],
  coding: [
    { x: 68.5, y: 61, pose: "work", flip: false },
    { x: 41.2, y: 63.5, pose: "work", flip: true },
    { x: 81.5, y: 74, pose: "work", flip: false },
  ],
  review: [
    { x: 81.5, y: 74, pose: "work", flip: false },
    { x: 68.5, y: 61, pose: "work", flip: false },
  ],
  gate: [
    { x: 56.5, y: 90, pose: "work", flip: true },
    { x: 49, y: 78, pose: "work", flip: false },
  ],
};

/** Varied cozy spots so idle cats naturally sit, sketch, nap and play yarn. */
const IDLE_SPOTS: StationSlot[] = [
  { x: 24.5, y: 54.5, pose: "board", flip: false }, // Whiteboard sketch
  { x: 41.2, y: 63.5, pose: "work", flip: true }, // Left oak workstation
  { x: 19, y: 84, pose: "nap", flip: false }, // Cozy woven rug nap
  { x: 50, y: 70, pose: "play", flip: false }, // Center floor yarn play
  { x: 68.5, y: 61, pose: "work", flip: false }, // Upper-right dual monitor desk
  { x: 53, y: 56, pose: "nap", flip: true }, // Moonlight floor nap
  { x: 32, y: 76, pose: "play", flip: false }, // Front-left yarn play
  { x: 81.5, y: 74, pose: "work", flip: false }, // Lower-right review desk
  { x: 60.5, y: 69, pose: "play", flip: true }, // Right hardwood floor play
  { x: 56.5, y: 90, pose: "work", flip: true }, // Reception gatekeeper desk
];

const GEM_COLORS = [
  "#4f8bff",
  "#2dd4bf",
  "#e06d53",
  "#fbbf24",
  "#a78bfa",
  "#34d399",
];

const AMBIENT_LAMPS = [
  { left: "28.5%", top: "47.5%", delay: "0s" },
  { left: "67.6%", top: "39.5%", delay: "1.3s" },
  { left: "95.3%", top: "62.5%", delay: "2.4s" },
];

const AMBIENT_STARS = [
  { left: "61.5%", top: "9%", delay: "0.2s" },
  { left: "65.8%", top: "17%", delay: "1.4s" },
  { left: "62.2%", top: "24%", delay: "2.1s" },
  { left: "74.5%", top: "13%", delay: "0.7s" },
  { left: "78.8%", top: "21%", delay: "1.8s" },
  { left: "75.6%", top: "31%", delay: "0.9s" },
];

const AMBIENT_MOTES = [
  { left: "48%", top: "51%", delay: "0s" },
  { left: "54%", top: "55%", delay: "2.1s" },
  { left: "51%", top: "60%", delay: "4.2s" },
  { left: "36%", top: "44%", delay: "1.2s" },
];

const PET_EMOTES_ZH = ["♥ 喵~", "✨ 开心", "♪ 呼噜~", "♥ 蹭蹭"];
const PET_EMOTES_EN = ["♥ meow~", "✨ happy", "♪ purr~", "♥ nuzzle"];

type CatMotion = {
  x: number;
  y: number;
  pose: CatPose;
  flip: boolean;
  walking: boolean;
  walkMs: number;
};

type FloorRipple = {
  id: number;
  x: number;
  y: number;
};

type EmotePopup = {
  id: number;
  text: string;
};

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

function activityProgress(state: string): number {
  switch (state) {
    case "planning":
      return 38;
    case "researching":
      return 58;
    case "coding":
      return 76;
    case "reviewing":
      return 90;
    case "waiting_permission":
      return 64;
    case "completed":
      return 100;
    case "failed":
      return 45;
    default:
      return 24;
  }
}

function poseDescription(pose: CatPose): string {
  switch (pose) {
    case "work":
      return t("坐在工位敲代码中", "Typing at a desk");
    case "board":
      return t("在白板前画架构图", "Sketching at the whiteboard");
    case "nap":
      return t("蜷成一团打盹中", "Curled up taking a nap");
    case "play":
      return t("在地板上拨毛线球", "Playing with a yarn ball");
    default:
      return t("正在研发室溜达", "Strolling around the studio");
  }
}

function resolveClickTarget(pctX: number, pctY: number): StationSlot {
  // Snap cleanly onto landmarks when the user clicks near furniture
  if (pctX >= 17 && pctX <= 36 && pctY >= 34 && pctY <= 57) {
    return { x: 24.5, y: 54.5, pose: "board", flip: false };
  }
  if (pctX >= 33 && pctX <= 48 && pctY >= 52 && pctY <= 69) {
    return { x: 41.2, y: 63.5, pose: "work", flip: true };
  }
  if (pctX >= 63 && pctX <= 76 && pctY >= 47 && pctY <= 67) {
    return { x: 68.5, y: 61, pose: "work", flip: false };
  }
  if (pctX >= 76 && pctX <= 92 && pctY >= 62 && pctY <= 82) {
    return { x: 81.5, y: 74, pose: "work", flip: false };
  }
  if (pctX >= 47 && pctX <= 64 && pctY >= 81 && pctY <= 95) {
    return { x: 56.5, y: 90, pose: "work", flip: true };
  }
  if (pctX >= 12 && pctX <= 28 && pctY >= 76 && pctY <= 92) {
    return { x: 19, y: 84, pose: "nap", flip: false };
  }
  const x = Math.min(84, Math.max(16, pctX));
  const y = Math.min(89, Math.max(48, pctY));
  return { x, y, pose: "play", flip: pctX < 50 };
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
  const [snapshot, setSnapshot] = useState<StudioSnapshot | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [focusedThreadID, setFocusedThreadID] = useState("");
  const [selectedAgentID, setSelectedAgentID] = useState("");
  const [selectedArtifact, setSelectedArtifact] =
    useState<StudioArtifact | null>(null);
  const [decidingID, setDecidingID] = useState("");
  const [command, setCommand] = useState("");
  const [sending, setSending] = useState(false);
  const [panelOpen, setPanelOpen] = useState(true);
  const [sceneKey, setSceneKey] = useState(0);
  const [dispatchPulseKey, setDispatchPulseKey] = useState(0);

  // Per-cat animated motion state (position, pose, facing, walk transition)
  const [motions, setMotions] = useState<Record<string, CatMotion>>({});
  const motionsRef = useRef<Record<string, CatMotion>>({});
  motionsRef.current = motions;

  // Per-cat custom idle target (set by autonomous wander or user floor click)
  const [idleTargets, setIdleTargets] = useState<Record<string, StationSlot>>(
    {},
  );
  const idleTargetsRef = useRef<Record<string, StationSlot>>({});
  idleTargetsRef.current = idleTargets;

  // Interactive reaction bursts (jump + floating pixel heart/meow)
  const [reactingIDs, setReactingIDs] = useState<Record<string, number>>({});
  const [emotes, setEmotes] = useState<Record<string, EmotePopup>>({});
  const [ripples, setRipples] = useState<FloorRipple[]>([]);

  const arrivalTimers = useRef<Record<string, number>>({});
  const emoteSeq = useRef(1);
  const worldRef = useRef<HTMLDivElement | null>(null);

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
    setIdleTargets({});
    setMotions({});
    setSceneKey((k) => k + 1);
    void load(false);
  }, [load]);

  useEffect(() => {
    if (!workspaceID) return;
    const timer = window.setInterval(() => {
      void load(true);
    }, 3000);
    return () => window.clearInterval(timer);
  }, [workspaceID, load]);

  // Clean up pending walk-arrival timers on unmount
  useEffect(() => {
    const timers = arrivalTimers.current;
    return () => {
      for (const id of Object.values(timers)) {
        window.clearTimeout(id);
      }
    };
  }, []);

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

  // Move a specific cat smoothly with walking animation + arrival pose transition
  const walkCatTo = useCallback((agentID: string, target: StationSlot) => {
    const prev = motionsRef.current[agentID];
    if (arrivalTimers.current[agentID]) {
      window.clearTimeout(arrivalTimers.current[agentID]);
      delete arrivalTimers.current[agentID];
    }

    if (!prev) {
      setMotions((cur) => ({
        ...cur,
        [agentID]: {
          x: target.x,
          y: target.y,
          pose: target.pose,
          flip: target.flip,
          walking: false,
          walkMs: 0,
        },
      }));
      return;
    }

    const dist = Math.hypot(target.x - prev.x, target.y - prev.y);
    if (dist < 1.6) {
      setMotions((cur) => ({
        ...cur,
        [agentID]: {
          x: target.x,
          y: target.y,
          pose: target.pose,
          flip: target.flip,
          walking: false,
          walkMs: 260,
        },
      }));
      return;
    }

    const walkMs = Math.round(Math.min(2400, Math.max(580, dist * 34)));
    const walkFlip = target.x < prev.x;
    setMotions((cur) => ({
      ...cur,
      [agentID]: {
        x: target.x,
        y: target.y,
        pose: "walk",
        flip: walkFlip,
        walking: true,
        walkMs,
      },
    }));

    arrivalTimers.current[agentID] = window.setTimeout(() => {
      delete arrivalTimers.current[agentID];
      setMotions((cur) => {
        const latest = cur[agentID];
        if (!latest) return cur;
        return {
          ...cur,
          [agentID]: {
            ...latest,
            pose: target.pose,
            flip: target.flip,
            walking: false,
          },
        };
      });
    }, walkMs);
  }, []);

  // Compute desired station/spot for every visible cat & trigger smooth walks
  const desiredSlots = useMemo(() => {
    const usedByStation: Record<string, number> = {};
    const result: Record<string, StationSlot> = {};
    visibleAgents.forEach((agent, idx) => {
      const activeKey =
        agent.station && ACTIVE_STATIONS[agent.station]
          ? agent.station
          : undefined;
      if (activeKey && stateKind(agent.state) !== "idle") {
        const slots = ACTIVE_STATIONS[activeKey];
        const slotIndex = usedByStation[activeKey] ?? 0;
        usedByStation[activeKey] = slotIndex + 1;
        const baseSlot = slots[slotIndex % slots.length];
        const overflow = Math.floor(slotIndex / slots.length);
        result[agent.agent_id] = {
          ...baseSlot,
          x: Math.min(85, Math.max(16, baseSlot.x + overflow * 4)),
          y: Math.min(90, Math.max(44, baseSlot.y + overflow * 3)),
        };
      } else {
        const custom = idleTargets[agent.agent_id];
        result[agent.agent_id] = custom ?? IDLE_SPOTS[idx % IDLE_SPOTS.length];
      }
    });
    return result;
  }, [visibleAgents, idleTargets]);

  useEffect(() => {
    for (const agent of visibleAgents) {
      const desired = desiredSlots[agent.agent_id];
      if (!desired) continue;
      const cur = motionsRef.current[agent.agent_id];
      if (
        !cur ||
        Math.hypot(cur.x - desired.x, cur.y - desired.y) > 0.6 ||
        (!cur.walking &&
          (cur.pose !== desired.pose || cur.flip !== desired.flip))
      ) {
        walkCatTo(agent.agent_id, desired);
      }
    }
  }, [visibleAgents, desiredSlots, walkCatTo]);

  // Autonomous cozy life loop: idle cats periodically stroll to free spots
  // to nap, play with yarn, sketch on the whiteboard, or tap at a desk.
  useEffect(() => {
    if (visibleAgents.length === 0) return;
    const timer = window.setInterval(() => {
      const idleCats = visibleAgents.filter(
        (ag) => stateKind(ag.state) === "idle",
      );
      if (idleCats.length === 0) return;

      const chosen = idleCats[Math.floor(Math.random() * idleCats.length)];
      const curMotion = motionsRef.current[chosen.agent_id];
      if (curMotion?.walking) return;

      // Avoid spots already occupied by other cats
      const occupied = visibleAgents
        .filter((ag) => ag.agent_id !== chosen.agent_id)
        .map((ag) => motionsRef.current[ag.agent_id])
        .filter((m): m is CatMotion => Boolean(m));

      const candidates = IDLE_SPOTS.filter((spot) => {
        if (
          curMotion &&
          Math.hypot(spot.x - curMotion.x, spot.y - curMotion.y) < 4
        ) {
          return false;
        }
        return !occupied.some(
          (other) => Math.hypot(spot.x - other.x, spot.y - other.y) < 8,
        );
      });
      if (candidates.length === 0) return;

      const nextSpot =
        candidates[Math.floor(Math.random() * candidates.length)];
      setIdleTargets((prev) => ({ ...prev, [chosen.agent_id]: nextSpot }));
    }, 5200);
    return () => window.clearInterval(timer);
  }, [visibleAgents]);

  const triggerCatReaction = useCallback(
    (agentID: string, customEmote?: string) => {
      const seq = emoteSeq.current++;
      const pool = lang === "en" ? PET_EMOTES_EN : PET_EMOTES_ZH;
      const text = customEmote ?? pool[seq % pool.length];
      setReactingIDs((prev) => ({ ...prev, [agentID]: seq }));
      setEmotes((prev) => ({ ...prev, [agentID]: { id: seq, text } }));
      window.setTimeout(() => {
        setReactingIDs((prev) => {
          if (prev[agentID] !== seq) return prev;
          const copy = { ...prev };
          delete copy[agentID];
          return copy;
        });
      }, 600);
      window.setTimeout(() => {
        setEmotes((prev) => {
          if (prev[agentID]?.id !== seq) return prev;
          const copy = { ...prev };
          delete copy[agentID];
          return copy;
        });
      }, 1200);
    },
    [lang],
  );

  const placedCats = useMemo(() => {
    return visibleAgents.map((agent, idx) => {
      const fallback = IDLE_SPOTS[idx % IDLE_SPOTS.length];
      const motion = motions[agent.agent_id] ?? {
        x: fallback.x,
        y: fallback.y,
        pose: fallback.pose,
        flip: fallback.flip,
        walking: false,
        walkMs: 0,
      };
      return {
        agent,
        cat: resolveStudioCat(agent.avatar, agent.handle || agent.display_name),
        motion,
        index: idx,
      };
    });
  }, [visibleAgents, motions]);

  const positionByAgentID = useMemo(() => {
    const map = new Map<string, { x: number; y: number }>();
    for (const item of placedCats) {
      map.set(item.agent.agent_id, { x: item.motion.x, y: item.motion.y });
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

  const handleWorldClick = (event: MouseEvent<HTMLDivElement>) => {
    if (!worldRef.current) return;
    const rect = worldRef.current.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return;
    const pctX = ((event.clientX - rect.left) / rect.width) * 100;
    const pctY = ((event.clientY - rect.top) / rect.height) * 100;

    // Pick selected idle cat, or fall back to first idle cat
    const targetCat =
      (selectedAgent && stateKind(selectedAgent.state) === "idle"
        ? selectedAgent
        : visibleAgents.find((ag) => stateKind(ag.state) === "idle")) ?? null;

    const rippleID = emoteSeq.current++;
    setRipples((prev) => [...prev.slice(-3), { id: rippleID, x: pctX, y: pctY }]);
    window.setTimeout(() => {
      setRipples((prev) => prev.filter((item) => item.id !== rippleID));
    }, 900);

    if (!targetCat) return;
    const slot = resolveClickTarget(pctX, pctY);
    setSelectedAgentID(targetCat.agent_id);
    setSelectedArtifact(null);
    setIdleTargets((prev) => ({ ...prev, [targetCat.agent_id]: slot }));
  };

  const assignQuickAction = (
    agentID: string,
    kind: "pet" | "work" | "board" | "play" | "nap",
  ) => {
    if (kind === "pet") {
      triggerCatReaction(agentID);
      return;
    }
    const presets: Record<"work" | "board" | "play" | "nap", StationSlot> = {
      work: { x: 41.2, y: 63.5, pose: "work", flip: true },
      board: { x: 24.5, y: 54.5, pose: "board", flip: false },
      play: { x: 50, y: 70, pose: "play", flip: false },
      nap: { x: 19, y: 84, pose: "nap", flip: false },
    };
    setIdleTargets((prev) => ({ ...prev, [agentID]: presets[kind] }));
    triggerCatReaction(
      agentID,
      kind === "nap"
        ? "Zzz..."
        : kind === "play"
          ? "🧶 ♪"
          : kind === "board"
            ? "💡"
            : "</>",
    );
  };

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
    setDispatchPulseKey((k) => k + 1);
    const leadCat = selectedAgent ?? visibleAgents[0];
    if (leadCat) {
      triggerCatReaction(leadCat.agent_id, t("! 收到任务", "! On it"));
    }
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

  return (
    <div className={cx("studio-page")} data-testid="studio-page">
      {/* Sleek unified top bar matching "Cocogem / Studio  [Thread]   ● Live" */}
      <header className={cx("studio-topbar")}>
        <div className={cx("studio-topbar-left")}>
          <span className={cx("studio-brand-crumb")}>
            <strong>Cocogem</strong>
            <span className={cx("studio-brand-sep")}>/</span>
            <span className={cx("studio-brand-sub")}>
              {t("猫猫研发室", "Studio")}
            </span>
          </span>
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
            onChange={(event) => {
              setFocusedThreadID(event.target.value);
              setSceneKey((k) => k + 1);
            }}
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
              className={cx("studio-open-btn")}
              onClick={() => onOpenThread(workspaceID, focusedThread.id)}
            >
              <Icon name="open" size={13} />
              {t("进入会话", "Open thread")}
            </button>
          )}
        </div>

        <div className={cx("studio-topbar-right")}>
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
                  `${visiblePermissions.length} pending`,
                )
              : activeCount > 0
                ? t(
                    `${activeCount} 只猫猫协作中`,
                    `${activeCount} active`,
                  )
                : t("Live · 研发室在线", "Live")}
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
            aria-label={
              panelOpen
                ? t("收起研发室浮窗", "Hide studio overlay")
                : t("展开研发室浮窗", "Show studio overlay")
            }
            title={
              panelOpen
                ? t("收起浮窗看全图", "Hide overlay")
                : t("展开浮窗", "Show overlay")
            }
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

      {/* Full-bleed edge-to-edge live pixel studio viewport */}
      <div className={cx("studio-viewport")}>
        <div
          key={`scene-${sceneKey}`}
          ref={worldRef}
          className={cx(
            `studio-world ${sceneKey > 1 ? "switching" : ""}`,
          )}
          role="img"
          aria-label={t(
            "Cocogem 像素猫猫研发室实时场景",
            "Cocogem Pixel Cat R&D Studio live scene",
          )}
          onClick={handleWorldClick}
        >
          <img
            src={roomBg}
            alt=""
            draggable={false}
            className={cx("studio-room-bg")}
          />

          {/* Ambient night-shift lighting: pulsing desk lamps, twinkling stars, motes */}
          <div className={cx("studio-ambient-layer")} aria-hidden="true">
            {AMBIENT_LAMPS.map((lamp, idx) => (
              <span
                key={`lamp-${idx}`}
                className={cx("studio-lamp-glow")}
                style={{
                  left: lamp.left,
                  top: lamp.top,
                  animationDelay: lamp.delay,
                }}
              />
            ))}
            {AMBIENT_STARS.map((star, idx) => (
              <span
                key={`star-${idx}`}
                className={cx("studio-star")}
                style={{
                  left: star.left,
                  top: star.top,
                  animationDelay: star.delay,
                }}
              />
            ))}
            {AMBIENT_MOTES.map((mote, idx) => (
              <span
                key={`mote-${idx}`}
                className={cx("studio-mote")}
                style={{
                  left: mote.left,
                  top: mote.top,
                  animationDelay: mote.delay,
                }}
              />
            ))}
          </div>

          {/* Animated glowing teal A2A relay curves with traveling energy orbs */}
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
                const my = Math.min(from.y, to.y) - 10;
                const d = `M ${from.x} ${from.y - 4} Q ${mx} ${my} ${to.x} ${to.y - 4}`;
                return (
                  <g key={`${relay.run_id}-${relay.step_id}`}>
                    <path
                      d={d}
                      className={cx("studio-relay-path")}
                      markerEnd="url(#studio-relay-arrow)"
                    />
                    <circle r="0.85" className={cx("studio-relay-orb")}>
                      <animateMotion dur="1.6s" repeatCount="indefinite" path={d} />
                    </circle>
                  </g>
                );
              })}
            </svg>
          )}

          {/* Interactive glowing gems aligned inside the right-wall glass cabinet */}
          <div className={cx("studio-gem-shelf")}>
            {visibleArtifacts.slice(0, 6).map((art, i) => {
              const color = GEM_COLORS[i % GEM_COLORS.length];
              return (
                <button
                  key={`${art.id}-${art.version}`}
                  type="button"
                  className={cx("studio-gem-slot")}
                  style={{ animationDelay: `${i * 0.4}s` }}
                  title={`${art.title} (v${art.version})`}
                  aria-label={`${art.title} v${art.version}`}
                  onClick={(event) => {
                    event.stopPropagation();
                    setSelectedArtifact(art);
                    setPanelOpen(true);
                  }}
                >
                  <svg width="20" height="20" viewBox="0 0 16 16" aria-hidden="true">
                    <polygon
                      points="8,1 14,6 8,15 2,6"
                      fill={color}
                      stroke="#ffffff"
                      strokeOpacity="0.72"
                      strokeWidth="1"
                    />
                    <polygon
                      points="8,1 11,6 8,15 5,6"
                      fill="#ffffff"
                      opacity="0.3"
                    />
                  </svg>
                </button>
              );
            })}
          </div>

          {/* Click-to-walk target ripples */}
          {ripples.map((r) => (
            <span
              key={r.id}
              className={cx("studio-floor-ripple")}
              style={{ left: `${r.x}%`, top: `${r.y}%` }}
              aria-hidden="true"
            />
          ))}

          {/* Multi-pose Animated Pixel Cat Characters */}
          {placedCats.map(({ agent, cat, motion, index }) => {
            const isSelected = selectedAgent?.agent_id === agent.agent_id;
            const kind = stateKind(agent.state);
            const isReacting = Boolean(reactingIDs[agent.agent_id]);
            const emote = emotes[agent.agent_id];
            const showCodeChips =
              !motion.walking &&
              motion.pose === "work" &&
              (agent.state === "coding" || agent.state === "researching");
            const showLoupe =
              !motion.walking &&
              motion.pose === "work" &&
              agent.state === "reviewing";
            const showZzz = !motion.walking && motion.pose === "nap";

            return (
              <button
                key={agent.agent_id}
                type="button"
                className={cx("studio-cat-actor")}
                style={{
                  left: `${motion.x}%`,
                  top: `${motion.y}%`,
                  zIndex: Math.round(motion.y) + (isSelected ? 20 : 5),
                  transitionDuration: `${motion.walkMs}ms, ${motion.walkMs}ms, 180ms`,
                  animationDelay: `${index * 85}ms`,
                }}
                aria-pressed={isSelected}
                title={`${agent.display_name} (${t(cat.zhName, cat.enName)}) · ${stateLabel(agent.state)}`}
                onClick={(event) => {
                  event.stopPropagation();
                  setSelectedArtifact(null);
                  setSelectedAgentID(agent.agent_id);
                  triggerCatReaction(agent.agent_id);
                }}
              >
                {emote && (
                  <span
                    key={emote.id}
                    className={cx("studio-emote-burst")}
                    aria-hidden="true"
                  >
                    {emote.text}
                  </span>
                )}

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

                {motion.walking && (
                  <span className={cx("studio-walk-dust")} aria-hidden="true" />
                )}

                <span
                  className={cx(
                    `studio-cat-sprite-wrap pose-${motion.pose} ${isReacting ? "reacting" : ""}`,
                  )}
                >
                  <CatCharacter
                    preset={cat}
                    pose={motion.pose}
                    flip={motion.flip}
                    size={96}
                  />
                  {showCodeChips && (
                    <span className={cx("studio-prop-code")} aria-hidden="true">
                      <span className={cx("studio-code-chip")}>{"</>"}</span>
                      <span className={cx("studio-code-chip")}>{"{}"}</span>
                    </span>
                  )}
                  {showLoupe && (
                    <span
                      className={cx("studio-prop-loupe")}
                      aria-hidden="true"
                    >
                      <Icon name="search" size={12} />
                    </span>
                  )}
                  {showZzz && (
                    <span className={cx("studio-prop-zzz")} aria-hidden="true">
                      Zzz
                    </span>
                  )}
                </span>

                <span className={cx("studio-cat-tag")}>
                  <span className={cx(`studio-cat-dot ${kind}`)} />
                  <span>{agent.display_name}</span>
                </span>
              </button>
            );
          })}
        </div>

        {/* Subtle interactive hint pill on bottom-left */}
        <div className={cx("studio-hint-pill")} aria-hidden="true">
          {t(
            "点击猫猫抚摸 · 点击工位/白板/地毯派它过去",
            "Click a cat to pet · Click any desk, board or rug to walk there",
          )}
        </div>

        {/* Command dispatch energy pulse */}
        {dispatchPulseKey > 0 && (
          <span
            key={`pulse-${dispatchPulseKey}`}
            className={cx("studio-dispatch-pulse")}
            aria-hidden="true"
          />
        )}

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
              <span className={cx("studio-card-sub")}>
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

        {/* Floating bottom command bar */}
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
                    "Message studio or @mention…",
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

        {/* Floating translucent right overlay cards (Crew + Inspector & Gems) */}
        {panelOpen && (
          <aside className={cx("studio-overlay-rail")}>
            <section className={cx("studio-card")}>
              <div className={cx("studio-card-head")}>
                <h3>
                  {t("研发室成员", "Crew")} ({visibleAgents.length})
                </h3>
                <span className={cx("studio-card-sub")}>
                  {snapshot?.workspace_name}
                </span>
              </div>
              <div className={cx("studio-crew-list")}>
                {visibleAgents.map((agent, idx) => {
                  const kind = stateKind(agent.state);
                  const cat = resolveStudioCat(
                    agent.avatar,
                    agent.handle || agent.display_name,
                  );
                  const barTone =
                    kind !== "idle"
                      ? kind
                      : idx % 3 === 0
                        ? "coral"
                        : idx % 3 === 1
                          ? "active"
                          : "";
                  return (
                    <button
                      key={agent.agent_id}
                      type="button"
                      className={cx("studio-crew-row")}
                      aria-pressed={selectedAgent?.agent_id === agent.agent_id}
                      onClick={() => {
                        setSelectedArtifact(null);
                        setSelectedAgentID(agent.agent_id);
                        triggerCatReaction(agent.agent_id);
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
                            className={cx(`studio-bar-fill ${barTone}`)}
                            style={{
                              width: `${activityProgress(agent.state)}%`,
                            }}
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
                    className={cx("studio-gate-btn approve")}
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
                    @{selectedAgent.handle} ·{" "}
                    {selectedAgent.executor || "default"} / {selectedAgent.model}
                  </span>
                  {selectedAgent.activity_summary ? (
                    <p className={cx("studio-inspector-summary")}>
                      {selectedAgent.activity_summary}
                    </p>
                  ) : (
                    <p className={cx("studio-inspector-summary")}>
                      {poseDescription(
                        motions[selectedAgent.agent_id]?.pose ?? "work",
                      )}
                    </p>
                  )}
                  {stateKind(selectedAgent.state) === "idle" && (
                    <div className={cx("studio-action-chips")}>
                      <button
                        type="button"
                        className={cx("studio-action-chip")}
                        onClick={() =>
                          assignQuickAction(selectedAgent.agent_id, "pet")
                        }
                      >
                        {t("摸一摸", "Pet")}
                      </button>
                      <button
                        type="button"
                        className={cx("studio-action-chip")}
                        onClick={() =>
                          assignQuickAction(selectedAgent.agent_id, "work")
                        }
                      >
                        {t("去办公", "Desk")}
                      </button>
                      <button
                        type="button"
                        className={cx("studio-action-chip")}
                        onClick={() =>
                          assignQuickAction(selectedAgent.agent_id, "board")
                        }
                      >
                        {t("画白板", "Board")}
                      </button>
                      <button
                        type="button"
                        className={cx("studio-action-chip")}
                        onClick={() =>
                          assignQuickAction(selectedAgent.agent_id, "play")
                        }
                      >
                        {t("玩毛线", "Play")}
                      </button>
                      <button
                        type="button"
                        className={cx("studio-action-chip")}
                        onClick={() =>
                          assignQuickAction(selectedAgent.agent_id, "nap")
                        }
                      >
                        {t("打个盹", "Nap")}
                      </button>
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

                {visibleArtifacts.length > 0 && (
                  <>
                    <div className={cx("studio-card-head")}>
                      <h3>
                        {t("宝石展柜", "Gems")} ({visibleArtifacts.length})
                      </h3>
                    </div>
                    <div className={cx("studio-gem-list")}>
                      {visibleArtifacts.slice(0, 4).map((art) => (
                        <button
                          key={`${art.id}-${art.version}`}
                          type="button"
                          className={cx("studio-gem-item")}
                          onClick={() =>
                            onOpenThread(workspaceID, art.thread_id)
                          }
                        >
                          <span className={cx("studio-gem-title")}>
                            {art.title}
                          </span>
                          <span className={cx("studio-card-sub")}>
                            v{art.version}
                          </span>
                        </button>
                      ))}
                    </div>
                  </>
                )}
              </section>
            ) : null}
          </aside>
        )}
      </div>
    </div>
  );
}
