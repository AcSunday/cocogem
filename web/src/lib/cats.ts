import type { Agent } from "../api/client";

export type CatBreedId =
  | "ragdoll"
  | "siamese"
  | "maine"
  | "orange"
  | "tabby"
  | "tuxedo"
  | "calico"
  | "british"
  | "silver"
  | "black";

export type CatAvatarValue = `cat:${CatBreedId}`;

export interface CatPreset {
  id: CatBreedId;
  value: CatAvatarValue;
  zhName: string;
  enName: string;
  zhTag: string;
  enTag: string;
  /** Portrait card tint for dark/light surfaces. */
  bg: string;
  /** Primary fur color. */
  coat: string;
  /** Darker shade for ears, stripes, tail tip or seal points. */
  accent: string;
  /** Secondary patch / belly / muzzle tone. */
  light: string;
  /** Optional third patch color (e.g. calico orange + charcoal). */
  patch?: string;
  /** Eye color. */
  eye: string;
  /** Nose / blush color. */
  nose: string;
  /** Optional collar accent in Studio full-body sprites. */
  collar: string;
  /** Marking pattern rendered on both portraits and Studio sprites. */
  pattern: "point" | "mask" | "mane" | "stripes" | "tuxedo" | "calico" | "solid" | "shaded";
}

export const CAT_PRESETS: readonly CatPreset[] = [
  {
    id: "ragdoll",
    value: "cat:ragdoll",
    zhName: "布偶猫",
    enName: "Ragdoll",
    zhTag: "蓝眼双色",
    enTag: "Blue-eyed",
    bg: "#1d2738",
    coat: "#f6f0e6",
    accent: "#6e513e",
    light: "#fffbf5",
    eye: "#38bdf8",
    nose: "#f3a8b0",
    collar: "#3b82f6",
    pattern: "point",
  },
  {
    id: "siamese",
    value: "cat:siamese",
    zhName: "暹罗猫",
    enName: "Siamese",
    zhTag: "重点色",
    enTag: "Seal-point",
    bg: "#26211e",
    coat: "#ebd8bc",
    accent: "#3b2920",
    light: "#f6ead7",
    eye: "#60a5fa",
    nose: "#2c1e18",
    collar: "#2dd4bf",
    pattern: "mask",
  },
  {
    id: "maine",
    value: "cat:maine",
    zhName: "缅因猫",
    enName: "Maine Coon",
    zhTag: "蓬松长毛",
    enTag: "Tufted mane",
    bg: "#28201a",
    coat: "#a87449",
    accent: "#543722",
    light: "#f3e3cc",
    eye: "#a3e635",
    nose: "#d97762",
    collar: "#e06d53",
    pattern: "mane",
  },
  {
    id: "orange",
    value: "cat:orange",
    zhName: "橘猫",
    enName: "Orange Tabby",
    zhTag: "元气橘纹",
    enTag: "Ginger",
    bg: "#2e2017",
    coat: "#ef8d38",
    accent: "#c45a16",
    light: "#fdebd3",
    eye: "#fbbf24",
    nose: "#f28b82",
    collar: "#f59e0b",
    pattern: "stripes",
  },
  {
    id: "tabby",
    value: "cat:tabby",
    zhName: "狸花猫",
    enName: "Dragon Li",
    zhTag: "经典虎斑",
    enTag: "Wild tabby",
    bg: "#1e2421",
    coat: "#8d8272",
    accent: "#36312b",
    light: "#e2dcd2",
    eye: "#34d399",
    nose: "#c97a6b",
    collar: "#10b981",
    pattern: "stripes",
  },
  {
    id: "tuxedo",
    value: "cat:tuxedo",
    zhName: "奶牛猫",
    enName: "Tuxedo",
    zhTag: "黑白燕尾",
    enTag: "Black & white",
    bg: "#1b202b",
    coat: "#262b36",
    accent: "#151821",
    light: "#f8fafc",
    eye: "#bef264",
    nose: "#f4a4b4",
    collar: "#ef4444",
    pattern: "tuxedo",
  },
  {
    id: "calico",
    value: "cat:calico",
    zhName: "三花猫",
    enName: "Calico",
    zhTag: "三色拼块",
    enTag: "Tricolor",
    bg: "#2a221d",
    coat: "#f8f4ee",
    accent: "#eb7c34",
    light: "#ffffff",
    patch: "#2e323d",
    eye: "#f59e0b",
    nose: "#f39aa6",
    collar: "#ec4899",
    pattern: "calico",
  },
  {
    id: "british",
    value: "cat:british",
    zhName: "英短蓝猫",
    enName: "British Blue",
    zhTag: "圆脸蓝灰",
    enTag: "Plush blue",
    bg: "#1c2330",
    coat: "#748399",
    accent: "#4b586b",
    light: "#9aa8bc",
    eye: "#f97316",
    nose: "#475569",
    collar: "#6366f1",
    pattern: "solid",
  },
  {
    id: "silver",
    value: "cat:silver",
    zhName: "银渐层",
    enName: "Silver Shaded",
    zhTag: "银白翡翠眼",
    enTag: "Emerald eye",
    bg: "#182628",
    coat: "#e2e8f0",
    accent: "#8e9eb2",
    light: "#f8fafc",
    eye: "#2dd4bf",
    nose: "#e88f9a",
    collar: "#06b6d4",
    pattern: "shaded",
  },
  {
    id: "black",
    value: "cat:black",
    zhName: "玄猫",
    enName: "Black Cat",
    zhTag: "金瞳黑猫",
    enTag: "Golden eye",
    bg: "#1a1829",
    coat: "#1b1f29",
    accent: "#323a4f",
    light: "#2d3548",
    eye: "#facc15",
    nose: "#475569",
    collar: "#2dd4bf",
    pattern: "solid",
  },
] as const;

const presetByValue = new Map<string, CatPreset>(
  CAT_PRESETS.map((item) => [item.value, item]),
);
const presetById = new Map<string, CatPreset>(
  CAT_PRESETS.map((item) => [item.id, item]),
);

export function isCatAvatar(avatar?: string): avatar is CatAvatarValue {
  return !!avatar && presetByValue.has(avatar);
}

export function getCatPreset(avatar?: string): CatPreset | undefined {
  if (!avatar) return undefined;
  return presetByValue.get(avatar) ?? presetById.get(avatar);
}

/**
 * Resolves a member into one of the 10 pixel cat presets for Studio rendering.
 * Uses the explicit `cat:<breed>` avatar first, then falls back cleanly by
 * handle/name hints or deterministic hash so legacy members still appear in
 * the Pixel Cat Studio.
 */
export function resolveStudioCat(
  avatar?: string,
  hint?: string,
): CatPreset {
  const exact = getCatPreset(avatar);
  if (exact) return exact;
  const text = (hint ?? "").toLowerCase();
  if (text.includes("ragdoll") || text.includes("布偶")) return CAT_PRESETS[0];
  if (text.includes("siamese") || text.includes("暹罗")) return CAT_PRESETS[1];
  if (text.includes("maine") || text.includes("缅因")) return CAT_PRESETS[2];
  if (text.includes("orange") || text.includes("橘")) return CAT_PRESETS[3];
  if (text.includes("tabby") || text.includes("狸花")) return CAT_PRESETS[4];
  if (text.includes("tuxedo") || text.includes("奶牛")) return CAT_PRESETS[5];
  if (text.includes("calico") || text.includes("三花")) return CAT_PRESETS[6];
  if (text.includes("british") || text.includes("英短") || text.includes("蓝猫"))
    return CAT_PRESETS[7];
  if (text.includes("silver") || text.includes("渐层")) return CAT_PRESETS[8];
  if (text.includes("black") || text.includes("玄猫") || text.includes("黑猫"))
    return CAT_PRESETS[9];
  let hash = 2166136261;
  for (const ch of text) {
    hash = Math.imul(hash ^ ch.codePointAt(0)!, 16777619);
  }
  return CAT_PRESETS[(hash >>> 0) % CAT_PRESETS.length];
}

/** Returns a map of `cat:<breed>` -> owning agent display name (excluding `excludeId`). */
export function usedCatAvatarOwners(
  agents: readonly Agent[],
  excludeAgentId?: string,
): Map<CatAvatarValue, string> {
  const used = new Map<CatAvatarValue, string>();
  for (const agent of agents) {
    if (excludeAgentId && agent.id === excludeAgentId) continue;
    if (isCatAvatar(agent.avatar)) {
      used.set(agent.avatar, agent.display_name || agent.handle);
    }
  }
  return used;
}

/** Picks the first unused built-in cat preset for a new member, or "" if all 10 are taken. */
export function firstUnusedCatAvatar(
  agents: readonly Agent[],
  excludeAgentId?: string,
): CatAvatarValue | "" {
  const used = usedCatAvatarOwners(agents, excludeAgentId);
  const found = CAT_PRESETS.find((preset) => !used.has(preset.value));
  return found ? found.value : "";
}
