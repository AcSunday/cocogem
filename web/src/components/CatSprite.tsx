import portraitBlack from "../assets/cats/portrait-black.png";
import portraitBritish from "../assets/cats/portrait-british.png";
import portraitCalico from "../assets/cats/portrait-calico.png";
import portraitMaine from "../assets/cats/portrait-maine.png";
import portraitOrange from "../assets/cats/portrait-orange.png";
import portraitRagdoll from "../assets/cats/portrait-ragdoll.png";
import portraitSiamese from "../assets/cats/portrait-siamese.png";
import portraitSilver from "../assets/cats/portrait-silver.png";
import portraitTabby from "../assets/cats/portrait-tabby.png";
import portraitTuxedo from "../assets/cats/portrait-tuxedo.png";

import sheetBlack from "../assets/cats/sheet-black.png";
import sheetBritish from "../assets/cats/sheet-british.png";
import sheetCalico from "../assets/cats/sheet-calico.png";
import sheetMaine from "../assets/cats/sheet-maine.png";
import sheetOrange from "../assets/cats/sheet-orange.png";
import sheetRagdoll from "../assets/cats/sheet-ragdoll.png";
import sheetSiamese from "../assets/cats/sheet-siamese.png";
import sheetSilver from "../assets/cats/sheet-silver.png";
import sheetTabby from "../assets/cats/sheet-tabby.png";
import sheetTuxedo from "../assets/cats/sheet-tuxedo.png";

import {
  CAT_PRESETS,
  getCatPreset,
  type CatBreedId,
  type CatPreset,
} from "../lib/cats";

const PORTRAIT_MAP: Record<CatBreedId, string> = {
  ragdoll: portraitRagdoll,
  siamese: portraitSiamese,
  maine: portraitMaine,
  orange: portraitOrange,
  tabby: portraitTabby,
  tuxedo: portraitTuxedo,
  calico: portraitCalico,
  british: portraitBritish,
  silver: portraitSilver,
  black: portraitBlack,
};

/** 5-frame horizontal sprite strips: [0: walk, 1: work, 2: board, 3: nap, 4: play]. */
const SHEET_MAP: Record<CatBreedId, string> = {
  ragdoll: sheetRagdoll,
  siamese: sheetSiamese,
  maine: sheetMaine,
  orange: sheetOrange,
  tabby: sheetTabby,
  tuxedo: sheetTuxedo,
  calico: sheetCalico,
  british: sheetBritish,
  silver: sheetSilver,
  black: sheetBlack,
};

export type CatPose = "walk" | "work" | "board" | "nap" | "play";

const POSE_FRAME_INDEX: Record<CatPose, number> = {
  walk: 0,
  work: 1,
  board: 2,
  nap: 3,
  play: 4,
};

export function catPortraitSrc(preset: CatPreset | string): string {
  const cat =
    typeof preset === "string"
      ? (getCatPreset(preset) ?? CAT_PRESETS[0])
      : preset;
  return PORTRAIT_MAP[cat.id];
}

export function catSpriteSheetSrc(preset: CatPreset | string): string {
  const cat =
    typeof preset === "string"
      ? (getCatPreset(preset) ?? CAT_PRESETS[0])
      : preset;
  return SHEET_MAP[cat.id];
}

/**
 * High-craft 32-bit pixel-art portrait of one of Cocogem's 10 built-in cat
 * breeds. Shared across member avatars, picker cards and Studio crew rows.
 */
export function CatPortrait({
  preset,
  size = 36,
  className,
}: {
  preset: CatPreset | string;
  size?: number;
  className?: string;
}) {
  const cat =
    typeof preset === "string"
      ? (getCatPreset(preset) ?? CAT_PRESETS[0])
      : preset;
  return (
    <img
      src={PORTRAIT_MAP[cat.id]}
      width={size}
      height={size}
      alt=""
      aria-hidden="true"
      draggable={false}
      className={className}
      style={{
        width: "100%",
        height: "100%",
        objectFit: "contain",
        imageRendering: "pixelated",
        background: cat.bg,
        borderRadius: "inherit",
        display: "block",
      }}
    />
  );
}

/**
 * High-craft 32-bit multi-pose isometric pixel-art cat sprite rendered inside
 * the Pixel Cat R&D Studio. Switches instantaneously across 5 distinct poses
 * (walk, seated work, whiteboard drawing, curled nap, yarn play) from a single
 * preloaded 5-frame sprite strip per breed.
 */
export function CatCharacter({
  preset,
  pose = "walk",
  flip = false,
  size = 92,
  className,
}: {
  preset: CatPreset | string;
  pose?: CatPose;
  flip?: boolean;
  size?: number | string;
  className?: string;
}) {
  const cat =
    typeof preset === "string"
      ? (getCatPreset(preset) ?? CAT_PRESETS[0])
      : preset;
  const frameIndex = POSE_FRAME_INDEX[pose] ?? 0;

  return (
    <span
      className={className}
      data-pose={pose}
      style={{
        position: "relative",
        display: "inline-block",
        width: size,
        height: size,
        overflow: "hidden",
        transform: flip ? "scaleX(-1)" : undefined,
      }}
    >
      <img
        src={SHEET_MAP[cat.id]}
        width={720}
        height={144}
        alt=""
        aria-hidden="true"
        draggable={false}
        style={{
          display: "block",
          width: "500%",
          height: "100%",
          maxWidth: "none",
          imageRendering: "pixelated",
          transform: `translateX(-${frameIndex * 20}%)`,
        }}
      />
    </span>
  );
}
