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

import spriteBlack from "../assets/cats/sprite-black.png";
import spriteBritish from "../assets/cats/sprite-british.png";
import spriteCalico from "../assets/cats/sprite-calico.png";
import spriteMaine from "../assets/cats/sprite-maine.png";
import spriteOrange from "../assets/cats/sprite-orange.png";
import spriteRagdoll from "../assets/cats/sprite-ragdoll.png";
import spriteSiamese from "../assets/cats/sprite-siamese.png";
import spriteSilver from "../assets/cats/sprite-silver.png";
import spriteTabby from "../assets/cats/sprite-tabby.png";
import spriteTuxedo from "../assets/cats/sprite-tuxedo.png";

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

const SPRITE_MAP: Record<CatBreedId, string> = {
  ragdoll: spriteRagdoll,
  siamese: spriteSiamese,
  maine: spriteMaine,
  orange: spriteOrange,
  tabby: spriteTabby,
  tuxedo: spriteTuxedo,
  calico: spriteCalico,
  british: spriteBritish,
  silver: spriteSilver,
  black: spriteBlack,
};

export function catPortraitSrc(preset: CatPreset | string): string {
  const cat =
    typeof preset === "string"
      ? (getCatPreset(preset) ?? CAT_PRESETS[0])
      : preset;
  return PORTRAIT_MAP[cat.id];
}

export function catSpriteSrc(preset: CatPreset | string): string {
  const cat =
    typeof preset === "string"
      ? (getCatPreset(preset) ?? CAT_PRESETS[0])
      : preset;
  return SPRITE_MAP[cat.id];
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

export type CatSpritePose =
  | "idle"
  | "planning"
  | "researching"
  | "coding"
  | "reviewing"
  | "gate"
  | "failed";

/**
 * High-craft 32-bit isometric pixel-art full-body cat character rendered inside
 * the Pixel Cat R&D Studio, linked 1:1 with the member's CatPortrait breed.
 */
export function CatCharacter({
  preset,
  pose = "idle",
  size = 82,
  className,
}: {
  preset: CatPreset | string;
  pose?: CatSpritePose;
  size?: number;
  className?: string;
}) {
  const cat =
    typeof preset === "string"
      ? (getCatPreset(preset) ?? CAT_PRESETS[0])
      : preset;
  return (
    <span
      className={className}
      data-pose={pose}
      style={{
        position: "relative",
        display: "inline-flex",
        alignItems: "center",
        justifyContent: "center",
        width: size,
        height: size,
      }}
    >
      <img
        src={SPRITE_MAP[cat.id]}
        width={size}
        height={size}
        alt=""
        aria-hidden="true"
        draggable={false}
        style={{
          width: "100%",
          height: "100%",
          objectFit: "contain",
          imageRendering: "pixelated",
          filter: "drop-shadow(0 6px 6px rgba(0, 0, 0, 0.52))",
          display: "block",
        }}
      />
    </span>
  );
}
