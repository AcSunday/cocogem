import {
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type MouseEvent,
} from "react";
import roomBg from "../assets/studio/room-bg.webp";
import roomForeground from "../assets/studio/room-foreground.webp";
import type {
  StudioAgentActivity,
  StudioArtifact,
  StudioSnapshot,
} from "../api/client";
import { resolveStudioCat, type CatPreset } from "../lib/cats";
import { t } from "../lib/i18n";
import { toolLabel } from "../lib/presentation";
import { scoped } from "../styles/classes";
import studio from "../styles/studio.module.css";
import { CatCharacter, type CatPose } from "./CatSprite";
import { PixelSprite, type PixelSpriteName } from "./PixelSprite";

const cx = scoped(studio);

/** Cat frame width as a percentage of the room width (shared with CSS). */
export const CAT_WIDTH = 12.4;
/** The room artwork is 1920×1200; one percent of width is 1.6% of height. */
const ASPECT = 1.6;
const CAT_HEIGHT = CAT_WIDTH * ASPECT;

/** Where a cat stands, in percentages of the room artwork at its feet. */
export type Spot = {
  x: number;
  y: number;
  pose: CatPose;
  flip: boolean;
  /** Seated behind furniture: the foreground layer covers the lower body. */
  behind?: boolean;
  /** A chair back drawn over the seated sprite. */
  seat?: "review" | "window";
  /** Napping on top of furniture that belongs to the foreground layer. */
  onTop?: boolean;
};

// Anchors are calibrated against room-bg.webp (see the 1% calibration grid).
export const ROOM = {
  board: { x: 19.2, y: 47.6, pose: "board", flip: false },
  board2: { x: 25.4, y: 43.8, pose: "board", flip: false },
  desk: { x: 33.6, y: 53.4, pose: "work", flip: false, behind: true },
  window: {
    x: 58.2,
    y: 47.2,
    pose: "board",
    flip: true,
    behind: true,
    seat: "window",
  },
  review: {
    x: 70.2,
    y: 57.2,
    pose: "work",
    flip: false,
    behind: true,
    seat: "review",
  },
  gate: { x: 58.6, y: 80.6, pose: "work", flip: false, behind: true },
  gate2: { x: 64.4, y: 76.4, pose: "work", flip: true, behind: true },
  counter: { x: 61.2, y: 79.6, pose: "nap", flip: true, onTop: true },
  shelfNap: { x: 9.4, y: 67.4, pose: "nap", flip: false },
  floorNap: { x: 52.6, y: 70.4, pose: "nap", flip: true },
  floorPlay: { x: 61.6, y: 63.4, pose: "play", flip: true },
  rightPlay: { x: 86.6, y: 70.2, pose: "play", flip: true },
  leftPlay: { x: 13.6, y: 61.4, pose: "play", flip: false },
} satisfies Record<string, Spot>;

const ACTIVE_STATIONS: Record<string, readonly Spot[]> = {
  whiteboard: [ROOM.board, ROOM.board2],
  research: [ROOM.desk, ROOM.window],
  coding: [ROOM.desk, ROOM.window],
  review: [ROOM.review, ROOM.window],
  gate: [ROOM.gate, ROOM.gate2],
};

/** Free spots idle cats drift between. */
const IDLE_SPOTS: readonly Spot[] = [
  ROOM.review,
  ROOM.desk,
  ROOM.board,
  ROOM.floorNap,
  ROOM.counter,
  ROOM.floorPlay,
  ROOM.shelfNap,
  ROOM.leftPlay,
  ROOM.rightPlay,
  ROOM.window,
];

/** A few breeds have a favourite corner when nothing is going on. */
const IDLE_HOME: Partial<Record<CatPreset["id"], Spot>> = {
  orange: ROOM.board,
  maine: ROOM.desk,
  ragdoll: ROOM.review,
  tabby: ROOM.counter,
  siamese: ROOM.floorPlay,
  calico: ROOM.shelfNap,
  black: ROOM.floorNap,
};

/** Accepted deliverables sit on the floor of the wall cabinet. */
const GEM_SLOTS: { x: number; y: number; sprite: PixelSpriteName }[] = [
  { x: 67.6, y: 17.2, sprite: "gemDiamond" },
  { x: 71.3, y: 19.3, sprite: "gemEmerald" },
  { x: 75.0, y: 21.4, sprite: "gemRuby" },
  { x: 78.4, y: 23.3, sprite: "gemTopaz" },
];

const LAMPS = [
  { x: 35.3, y: 51.8, delay: "0s" },
  { x: 58.4, y: 23.6, delay: "1.4s" },
  { x: 88.3, y: 45.6, delay: "2.6s" },
];

const STARS = [
  { x: 51.6, y: 2.9, delay: "0.2s" },
  { x: 55.6, y: 9.4, delay: "1.6s" },
  { x: 60.8, y: 6.1, delay: "2.4s" },
  { x: 54.4, y: 15.3, delay: "0.9s" },
  { x: 87.6, y: 17.1, delay: "1.2s" },
  { x: 93.2, y: 21.4, delay: "2.9s" },
  { x: 88.8, y: 31.6, delay: "0.5s" },
  { x: 95.1, y: 36.2, delay: "1.9s" },
];

const MOTES = [
  { x: 36, y: 44, delay: "0s" },
  { x: 47, y: 52, delay: "2.2s" },
  { x: 60, y: 31, delay: "4.1s" },
  { x: 74, y: 44, delay: "1.3s" },
  { x: 22, y: 34, delay: "3.3s" },
];

export type StateKind = "active" | "warn" | "error" | "done" | "idle";

export function stateKind(state: string): StateKind {
  switch (state) {
    case "waiting_permission":
      return "warn";
    case "failed":
      return "error";
    case "completed":
      return "done";
    case "planning":
    case "researching":
    case "coding":
    case "reviewing":
      return "active";
    default:
      return "idle";
  }
}

/** Colour family per role, mirroring the Logo 01 prism palette. */
export function stateTone(state: string): string {
  switch (state) {
    case "planning":
      return "coral";
    case "researching":
    case "coding":
      return "teal";
    case "reviewing":
      return "blue";
    case "waiting_permission":
      return "amber";
    case "failed":
      return "red";
    case "completed":
      return "green";
    default:
      return "idle";
  }
}

export function stateLabel(state: string): string {
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

export function poseDescription(pose?: CatPose): string {
  switch (pose) {
    case "work":
      return t("坐在工位上", "At the desk");
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

export const QUICK_SPOTS = {
  work: ROOM.desk,
  board: ROOM.board,
  play: ROOM.floorPlay,
  nap: ROOM.floorNap,
} satisfies Record<string, Spot>;

/** Maps a click on the room to the station or floor spot it shows. */
function spotAt(x: number, y: number): Spot | undefined {
  if (x >= 9 && x <= 28 && y >= 8 && y <= 50) return ROOM.board;
  if (x >= 28 && x <= 51 && y >= 38 && y <= 64) return ROOM.desk;
  if (x >= 52 && x <= 72 && y >= 20 && y <= 46) return ROOM.window;
  if (x >= 64 && x <= 92 && y >= 32 && y <= 62) return ROOM.review;
  if (x >= 49 && x <= 70 && y >= 67 && y <= 86) return ROOM.counter;
  // The open floor between the furniture.
  const floor =
    (y >= 58 && y <= 74 && x >= 50 && x <= 66) ||
    (y >= 58 && y <= 72 && x >= 6 && x <= 18) ||
    (y >= 64 && y <= 74 && x >= 76 && x <= 92);
  if (!floor) return undefined;
  return { x, y, pose: "play", flip: x < 50 };
}

type CatMotion = Spot & {
  walking: boolean;
  walkMs: number;
  /** Bumped on arrival so the landing squash replays. */
  arrived: number;
};

type Ripple = { id: number; x: number; y: number };
type Emote = { id: number; text: string };

const PET_EMOTES_ZH = ["♥ 喵~", "✨ 开心", "♪ 呼噜~", "♥ 蹭蹭"];
const PET_EMOTES_EN = ["♥ meow~", "✨ happy", "♪ purr~", "♥ nuzzle"];

export type PlacedCat = {
  agent: StudioAgentActivity;
  cat: CatPreset;
  motion: CatMotion;
};

function sameSpot(a: Spot, b: Spot) {
  return Math.hypot((a.x - b.x) * ASPECT, a.y - b.y) < 0.8;
}

/**
 * Places every visible member at a station (working) or a cozy idle spot and
 * animates the walk between spots. Positions persist across snapshots so a
 * refresh never teleports a cat that is already where it belongs.
 */
export function useStudioCats(
  agents: StudioAgentActivity[],
  reducedMotion: boolean,
) {
  const [motions, setMotions] = useState<Record<string, CatMotion>>({});
  const [idleTargets, setIdleTargets] = useState<Record<string, Spot>>({});
  const motionsRef = useRef(motions);
  motionsRef.current = motions;
  const agentsRef = useRef(agents);
  agentsRef.current = agents;
  const timers = useRef<Record<string, number>>({});

  useEffect(() => {
    const pending = timers.current;
    return () => {
      for (const id of Object.values(pending)) window.clearTimeout(id);
    };
  }, []);

  const walkTo = useCallback(
    (agentID: string, target: Spot) => {
      const previous = motionsRef.current[agentID];
      window.clearTimeout(timers.current[agentID]);
      delete timers.current[agentID];
      if (!previous || reducedMotion) {
        setMotions((current) => ({
          ...current,
          [agentID]: { ...target, walking: false, walkMs: 0, arrived: 0 },
        }));
        return;
      }
      const distance = Math.hypot(
        (target.x - previous.x) * ASPECT,
        target.y - previous.y,
      );
      if (distance < 2) {
        setMotions((current) => ({
          ...current,
          [agentID]: {
            ...target,
            walking: false,
            walkMs: 240,
            arrived: previous.arrived,
          },
        }));
        return;
      }
      const walkMs = Math.round(Math.min(2800, Math.max(700, distance * 52)));
      setMotions((current) => ({
        ...current,
        [agentID]: {
          ...target,
          pose: "walk",
          flip: target.x < previous.x,
          walking: true,
          walkMs,
          arrived: previous.arrived,
        },
      }));
      timers.current[agentID] = window.setTimeout(() => {
        delete timers.current[agentID];
        setMotions((current) => {
          const latest = current[agentID];
          if (!latest) return current;
          return {
            ...current,
            [agentID]: {
              ...latest,
              pose: target.pose,
              flip: target.flip,
              walking: false,
              arrived: latest.arrived + 1,
            },
          };
        });
      }, walkMs);
    },
    [reducedMotion],
  );

  const desired = useMemo(() => {
    const result: Record<string, Spot> = {};
    const taken: Spot[] = [];
    const free = (spot: Spot) =>
      taken.every(
        (other) => Math.hypot((spot.x - other.x) * ASPECT, spot.y - other.y) > 7,
      );
    const claim = (agent: StudioAgentActivity, preferred: readonly Spot[]) => {
      const spot =
        [...preferred, ...IDLE_SPOTS].find(free) ?? preferred[0] ?? ROOM.floorPlay;
      taken.push(spot);
      result[agent.agent_id] = spot;
    };
    const working = agents.filter(
      (agent) =>
        stateKind(agent.state) !== "idle" &&
        stateKind(agent.state) !== "done" &&
        ACTIVE_STATIONS[agent.station],
    );
    // The gate first: an approval must always be visible at the counter.
    working
      .sort(
        (a, b) =>
          Number(b.state === "waiting_permission") -
          Number(a.state === "waiting_permission"),
      )
      .forEach((agent) => claim(agent, ACTIVE_STATIONS[agent.station]));
    agents.forEach((agent, index) => {
      if (result[agent.agent_id]) return;
      const breed = resolveStudioCat(
        agent.avatar,
        agent.handle || agent.display_name,
      ).id;
      const home = IDLE_HOME[breed] ?? IDLE_SPOTS[index % IDLE_SPOTS.length];
      claim(agent, [idleTargets[agent.agent_id] ?? home]);
    });
    return result;
  }, [agents, idleTargets]);

  useEffect(() => {
    for (const agent of agents) {
      const target = desired[agent.agent_id];
      const current = motionsRef.current[agent.agent_id];
      if (!target) continue;
      if (
        !current ||
        !sameSpot(current, target) ||
        (!current.walking &&
          (current.pose !== target.pose || current.flip !== target.flip))
      ) {
        walkTo(agent.agent_id, target);
      }
    }
  }, [agents, desired, walkTo]);

  // Idle cats potter about on their own: nap, play, doodle or sit at a desk.
  useEffect(() => {
    if (reducedMotion || agents.length === 0) return;
    const timer = window.setInterval(() => {
      const idle = agentsRef.current.filter(
        (agent) => stateKind(agent.state) === "idle",
      );
      if (idle.length === 0) return;
      const chosen = idle[Math.floor(Math.random() * idle.length)];
      const motion = motionsRef.current[chosen.agent_id];
      if (!motion || motion.walking) return;
      const others = agentsRef.current
        .filter((agent) => agent.agent_id !== chosen.agent_id)
        .map((agent) => motionsRef.current[agent.agent_id])
        .filter((item): item is CatMotion => Boolean(item));
      const options = IDLE_SPOTS.filter(
        (spot) =>
          !sameSpot(spot, motion) &&
          others.every(
            (other) =>
              Math.hypot((spot.x - other.x) * ASPECT, spot.y - other.y) > 9,
          ),
      );
      if (options.length === 0) return;
      const next = options[Math.floor(Math.random() * options.length)];
      setIdleTargets((current) => ({ ...current, [chosen.agent_id]: next }));
    }, 6400);
    return () => window.clearInterval(timer);
  }, [agents.length, reducedMotion]);

  const placed = useMemo<PlacedCat[]>(
    () =>
      agents.map((agent) => ({
        agent,
        cat: resolveStudioCat(agent.avatar, agent.handle || agent.display_name),
        motion: motions[agent.agent_id] ?? {
          ...(desired[agent.agent_id] ?? ROOM.floorPlay),
          walking: false,
          walkMs: 0,
          arrived: 0,
        },
      })),
    [agents, motions, desired],
  );

  const sendTo = useCallback((agentID: string, spot: Spot) => {
    setIdleTargets((current) => ({ ...current, [agentID]: spot }));
  }, []);

  const reset = useCallback(() => {
    setIdleTargets({});
    setMotions({});
  }, []);

  return { placed, sendTo, reset };
}

/** Where floating props sit relative to the feet, in % of the room height. */
function headOffset(pose: CatPose) {
  switch (pose) {
    case "nap":
      return CAT_HEIGHT * 0.5;
    case "board":
      return CAT_HEIGHT * 0.9;
    default:
      return CAT_HEIGHT * 0.86;
  }
}

function activityProps(
  agent: StudioAgentActivity,
  motion: CatMotion,
): { sprites: PixelSpriteName[]; label: string } | undefined {
  if (motion.walking) return undefined;
  switch (agent.state) {
    case "planning":
      return { sprites: ["think"], label: t("拆解目标", "Planning") };
    case "researching":
      return { sprites: ["loupe"], label: t("查资料", "Researching") };
    case "coding":
      return { sprites: ["code"], label: t("写代码", "Coding") };
    case "reviewing":
      return { sprites: ["loupe"], label: t("审 Diff", "Reviewing") };
    case "failed":
      return { sprites: ["alert"], label: t("遇到问题", "Blocked") };
    case "completed":
      return { sprites: ["check"], label: t("已交付", "Delivered") };
    default:
      return undefined;
  }
}

type Permission = StudioSnapshot["pending_permissions"][number];

export function StudioScene({
  cats,
  relays,
  artifacts,
  permission,
  permissionCount,
  selectedAgentID,
  selectedArtifactID,
  reducedMotion,
  emotes,
  reacting,
  onSelectAgent,
  onSelectArtifact,
  onReviewPermission,
  onFloor,
}: {
  cats: PlacedCat[];
  relays: StudioSnapshot["relays"];
  artifacts: StudioArtifact[];
  permission?: Permission;
  permissionCount: number;
  selectedAgentID: string;
  selectedArtifactID: string;
  reducedMotion: boolean;
  emotes: Record<string, Emote>;
  reacting: Record<string, number>;
  onSelectAgent: (agentID: string) => void;
  onSelectArtifact: (artifact: StudioArtifact) => void;
  onReviewPermission: (permission: Permission) => void;
  /** A click on open floor or a station; false when nothing moved. */
  onFloor: (spot: Spot) => boolean;
}) {
  const [ripples, setRipples] = useState<Ripple[]>([]);
  const rippleSeq = useRef(1);
  const markerID = useId();

  const seated = useMemo(() => {
    const map = new Map<string, { x: number; y: number }>();
    for (const item of cats) {
      if (item.motion.walking) continue;
      map.set(item.agent.agent_id, {
        x: item.motion.x,
        y: item.motion.y - headOffset(item.motion.pose) * 0.62,
      });
    }
    return map;
  }, [cats]);

  const gateCat = permission
    ? cats.find(
        (item) =>
          item.agent.agent_id === permission.request.member_id ||
          (item.agent.state === "waiting_permission" &&
            item.agent.thread_id === permission.request.thread_id),
      )
    : undefined;
  const gateAnchor =
    gateCat && !gateCat.motion.walking
      ? {
          x: gateCat.motion.x,
          y: gateCat.motion.y - headOffset(gateCat.motion.pose) - 1.6,
        }
      : permission
        ? { x: ROOM.gate.x, y: ROOM.gate.y - CAT_HEIGHT * 0.9 }
        : undefined;

  const boardBusy = cats.some(
    (item) =>
      !item.motion.walking &&
      item.agent.state === "planning" &&
      item.motion.pose === "board",
  );

  const handleClick = (event: MouseEvent<HTMLDivElement>) => {
    const rect = event.currentTarget.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return;
    const x = ((event.clientX - rect.left) / rect.width) * 100;
    const y = ((event.clientY - rect.top) / rect.height) * 100;
    const spot = spotAt(x, y);
    if (!spot || !onFloor(spot) || reducedMotion) return;
    const id = rippleSeq.current++;
    setRipples((current) => [...current.slice(-3), { id, x: spot.x, y: spot.y }]);
    window.setTimeout(
      () => setRipples((current) => current.filter((item) => item.id !== id)),
      900,
    );
  };

  return (
    <div
      className={cx("studio-world")}
      style={{ "--cat-w": CAT_WIDTH } as CSSProperties}
      role="group"
      aria-label={t(
        "Cocogem 像素猫猫研发室实时场景",
        "Cocogem Pixel Cat R&D Studio live scene",
      )}
      onClick={handleClick}
    >
      <img src={roomBg} alt="" draggable={false} className={cx("studio-room-bg")} />

      <div className={cx("studio-ambient-back")} aria-hidden="true">
        {STARS.map((star, index) => (
          <span
            key={`star-${index}`}
            className={cx("studio-star")}
            style={{ left: `${star.x}%`, top: `${star.y}%`, animationDelay: star.delay }}
          />
        ))}
        <span className={cx("studio-shooting-star")} />
        {LAMPS.map((lamp, index) => (
          <span
            key={`lamp-${index}`}
            className={cx("studio-lamp-glow")}
            style={{ left: `${lamp.x}%`, top: `${lamp.y}%`, animationDelay: lamp.delay }}
          />
        ))}
        <span className={cx("studio-board-glow", boardBusy && "busy")} />
      </div>

      {artifacts.slice(0, GEM_SLOTS.length).map((artifact, index) => {
        const slot = GEM_SLOTS[index];
        return (
          <button
            key={`${artifact.id}-${artifact.version}`}
            type="button"
            className={cx("studio-gem")}
            style={{
              left: `${slot.x}%`,
              top: `${slot.y}%`,
              animationDelay: `${index * 0.6}s`,
            }}
            aria-pressed={selectedArtifactID === artifact.id}
            aria-label={`${artifact.title} v${artifact.version}`}
            title={`${artifact.title} · v${artifact.version}`}
            onClick={(event) => {
              event.stopPropagation();
              onSelectArtifact(artifact);
            }}
          >
            <PixelSprite name={slot.sprite} />
          </button>
        );
      })}
      {artifacts.length > GEM_SLOTS.length && (
        <span className={cx("studio-gem-more")} aria-hidden="true">
          +{artifacts.length - GEM_SLOTS.length}
        </span>
      )}

      {ripples.map((ripple) => (
        <span
          key={ripple.id}
          className={cx("studio-floor-ripple")}
          style={{ left: `${ripple.x}%`, top: `${ripple.y}%` }}
          aria-hidden="true"
        />
      ))}

      {cats.map(({ agent, cat, motion }) => {
        const selected = agent.agent_id === selectedAgentID;
        const resting = !motion.walking;
        const behind = resting && motion.behind;
        const layer = behind
          ? 20 + Math.round(motion.y / 10)
          : 60 + Math.round(motion.y);
        return (
          <button
            key={agent.agent_id}
            type="button"
            className={cx("studio-cat")}
            data-pose={motion.pose}
            data-seat={resting ? motion.seat : undefined}
            data-walking={motion.walking || undefined}
            data-selected={selected || undefined}
            data-tone={stateTone(agent.state)}
            style={{
              left: `${motion.x}%`,
              top: `${motion.y}%`,
              zIndex: resting && motion.onTop ? 160 : layer,
              transitionDuration: `${motion.walkMs}ms`,
            }}
            aria-pressed={selected}
            aria-label={agent.display_name}
            title={`${agent.display_name} · ${t(cat.zhName, cat.enName)} · ${stateLabel(agent.state)}`}
            onClick={(event) => {
              event.stopPropagation();
              onSelectAgent(agent.agent_id);
            }}
          >
            <span className={cx("studio-cat-shadow")} aria-hidden="true" />
            <span
              key={`${motion.arrived}-${reacting[agent.agent_id] ?? 0}`}
              className={cx(
                "studio-cat-body",
                reacting[agent.agent_id] ? "reacting" : "",
                motion.arrived > 0 && resting ? "landed" : "",
              )}
            >
              <CatCharacter
                preset={cat}
                pose={motion.pose}
                flip={motion.flip}
                size="100%"
              />
            </span>
          </button>
        );
      })}

      <img
        src={roomBg}
        alt=""
        draggable={false}
        className={cx("studio-chair-front review")}
      />
      <img
        src={roomBg}
        alt=""
        draggable={false}
        className={cx("studio-chair-front window")}
      />
      <img
        src={roomForeground}
        alt=""
        draggable={false}
        className={cx("studio-foreground")}
      />

      {relays.length > 0 && (
        <svg
          viewBox="0 0 160 100"
          className={cx("studio-relays")}
          aria-hidden="true"
        >
          <defs>
            <marker
              id={markerID}
              markerWidth="3.2"
              markerHeight="3.2"
              markerUnits="userSpaceOnUse"
              viewBox="0 0 4 4"
              refX="3.2"
              refY="2"
              orient="auto"
            >
              <path d="M0,0.4 L3.8,2 L0,3.6 Z" fill="#5eead4" />
            </marker>
          </defs>
          {relays.map((relay) => {
            const from = seated.get(relay.from_agent_id);
            const to = seated.get(relay.to_agent_id);
            if (!from || !to) return null;
            const x1 = from.x * ASPECT + 2.5;
            const x2 = to.x * ASPECT - 3;
            const y1 = from.y;
            const y2 = to.y;
            const mx = (x1 + x2) / 2;
            const my = Math.min(y1, y2) - 9;
            const d = `M ${x1} ${y1} Q ${mx} ${my} ${x2} ${y2}`;
            const live = relay.status !== "completed" && relay.status !== "succeeded";
            return (
              <g key={`${relay.run_id}-${relay.step_id}-${relay.from_agent_id}`}>
                <path d={d} className={cx("studio-relay-glow", live && "live")} />
                <path
                  d={d}
                  className={cx("studio-relay-path", live && "live")}
                  markerEnd={`url(#${markerID})`}
                />
                {live && !reducedMotion && (
                  <g className={cx("studio-relay-doc")}>
                    <animateMotion dur="2.6s" repeatCount="indefinite" path={d} />
                    <rect x="-1.6" y="-2" width="3.2" height="4" rx="0.4" />
                    <path d="M -0.9 -0.9 H 0.9 M -0.9 0.1 H 0.9 M -0.9 1.1 H 0.4" />
                  </g>
                )}
              </g>
            );
          })}
        </svg>
      )}

      {cats.map(({ agent, motion }) => {
        const props = activityProps(agent, motion);
        const emote = emotes[agent.agent_id];
        const napping = !motion.walking && motion.pose === "nap";
        const selected = agent.agent_id === selectedAgentID;
        if (!props && !emote && !napping && !selected) return null;
        const head = headOffset(motion.pose);
        return (
          <span
            key={`props-${agent.agent_id}`}
            className={cx("studio-cat-props")}
            data-pose={motion.pose}
            data-walking={motion.walking || undefined}
            style={{
              left: `${motion.x}%`,
              top: `${motion.y - head}%`,
              transitionDuration: `${motion.walkMs}ms`,
            }}
            aria-hidden="true"
          >
            {props && (
              <span
                className={cx("studio-activity")}
                data-state={agent.state}
                title={props.label}
              >
                {props.sprites.map((sprite, index) => (
                  <span
                    key={sprite}
                    className={cx("studio-activity-icon")}
                    data-sprite={sprite}
                    style={{ animationDelay: `${index * 0.45}s` }}
                  >
                    <PixelSprite name={sprite} />
                  </span>
                ))}
              </span>
            )}
            {napping && !props && <span className={cx("studio-zzz")}>z Z z</span>}
            {selected && (
              <span className={cx("studio-name-tag")} data-tone={stateTone(agent.state)}>
                {agent.display_name}
              </span>
            )}
            {emote && (
              <span key={emote.id} className={cx("studio-emote")}>
                {emote.text}
              </span>
            )}
          </span>
        );
      })}

      {permission && gateAnchor && (
        <div
          className={cx("studio-gate-bubble")}
          style={{ left: `${gateAnchor.x}%`, top: `${gateAnchor.y}%` }}
          role="region"
          aria-label={t("研发室待审批请求", "Studio pending approval")}
          onClick={(event) => event.stopPropagation()}
        >
          <span className={cx("studio-gate-dot")} aria-hidden="true" />
          <span className={cx("studio-gate-text")}>
            <strong>
              {t("门禁审批", "Permission")}
              {(permission.agent_name || gateCat?.agent.display_name) &&
                ` · ${permission.agent_name || gateCat?.agent.display_name}`}
            </strong>
            <span>
              {permission.request.operation_summary ||
                permission.request.reason ||
                toolLabel(permission.request.tool_name)}
            </span>
          </span>
          {permissionCount > 1 && (
            <span className={cx("studio-gate-count")}>+{permissionCount - 1}</span>
          )}
          <button
            type="button"
            className={cx("studio-gate-approve")}
            onClick={() => onReviewPermission(permission)}
          >
            {t("审批", "Review")}
          </button>
        </div>
      )}

      <div className={cx("studio-ambient-front")} aria-hidden="true">
        {MOTES.map((mote, index) => (
          <span
            key={`mote-${index}`}
            className={cx("studio-mote")}
            style={{ left: `${mote.x}%`, top: `${mote.y}%`, animationDelay: mote.delay }}
          />
        ))}
      </div>
    </div>
  );
}

export const STUDIO_PET_EMOTES = { zh: PET_EMOTES_ZH, en: PET_EMOTES_EN };
