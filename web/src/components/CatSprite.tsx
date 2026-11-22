import { CAT_PRESETS, getCatPreset, type CatPreset } from "../lib/cats";

/**
 * Crisp 16x16 pixel-art portrait of one of Cocogem's 10 built-in cat breeds.
 * Used as the member's portrait avatar across Chat, Tasks, Settings and Studio.
 */
export function CatPortrait({
  preset,
  size = 32,
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
  const outline = "#12141a";
  const earInner = cat.pattern === "mask" || cat.id === "black" ? "#4a3c42" : "#f2a6b3";
  const rightEarColor =
    cat.pattern === "calico"
      ? cat.patch ?? cat.accent
      : cat.pattern === "point" || cat.pattern === "mask" || cat.pattern === "tuxedo"
        ? cat.accent
        : cat.coat;
  const leftEarColor =
    cat.pattern === "calico" ||
    cat.pattern === "point" ||
    cat.pattern === "mask" ||
    cat.pattern === "tuxedo"
      ? cat.accent
      : cat.coat;

  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      shapeRendering="crispEdges"
      className={className}
      aria-hidden="true"
      focusable="false"
    >
      {/* Background tile */}
      <rect width="16" height="16" fill={cat.bg} />
      <rect x="0" y="13" width="16" height="3" fill={outline} opacity="0.28" />

      {/* Outer silhouette / pixel outline */}
      <rect x="2" y="2" width="3" height="4" fill={outline} />
      <rect x="11" y="2" width="3" height="4" fill={outline} />
      <rect x="3" y="4" width="10" height="10" fill={outline} />
      <rect x="2" y="6" width="12" height="7" fill={outline} />
      <rect x="4" y="14" width="8" height="1" fill={outline} />

      {/* Maine Coon ear tufts */}
      {cat.pattern === "mane" && (
        <>
          <rect x="2" y="1" width="1" height="1" fill={cat.accent} />
          <rect x="13" y="1" width="1" height="1" fill={cat.accent} />
        </>
      )}

      {/* Ears */}
      <rect x="3" y="3" width="2" height="3" fill={leftEarColor} />
      <rect x="11" y="3" width="2" height="3" fill={rightEarColor} />
      <rect x="4" y="4" width="1" height="2" fill={earInner} />
      <rect x="11" y="4" width="1" height="2" fill={earInner} />

      {/* Head base */}
      <rect x="4" y="5" width="8" height="8" fill={cat.coat} />
      <rect x="3" y="6" width="10" height="6" fill={cat.coat} />
      <rect x="5" y="13" width="6" height="1" fill={cat.coat} />

      {/* Breed-specific facial markings */}
      {cat.pattern === "point" && (
        <>
          {/* Ragdoll inverted V mask */}
          <rect x="3" y="6" width="3" height="4" fill={cat.accent} />
          <rect x="10" y="6" width="3" height="4" fill={cat.accent} />
          <rect x="4" y="5" width="3" height="2" fill={cat.accent} />
          <rect x="9" y="5" width="3" height="2" fill={cat.accent} />
          <rect x="6" y="7" width="4" height="6" fill={cat.light} />
          <rect x="5" y="10" width="6" height="3" fill={cat.light} />
        </>
      )}

      {cat.pattern === "mask" && (
        <>
          {/* Siamese dark seal-point face mask */}
          <rect x="4" y="7" width="8" height="5" fill={cat.accent} />
          <rect x="5" y="6" width="6" height="1" fill={cat.accent} />
          <rect x="6" y="12" width="4" height="1" fill={cat.accent} />
        </>
      )}

      {cat.pattern === "stripes" && (
        <>
          {/* Forehead M stripes + cheek stripes */}
          <rect x="6" y="5" width="1" height="2" fill={cat.accent} />
          <rect x="8" y="5" width="1" height="2" fill={cat.accent} />
          <rect x="9" y="5" width="1" height="2" fill={cat.accent} />
          <rect x="3" y="8" width="2" height="1" fill={cat.accent} />
          <rect x="11" y="8" width="2" height="1" fill={cat.accent} />
          <rect x="5" y="10" width="6" height="3" fill={cat.light} />
        </>
      )}

      {cat.pattern === "mane" && (
        <>
          {/* Maine Coon forehead stripes + fluffy cream mane */}
          <rect x="6" y="5" width="1" height="2" fill={cat.accent} />
          <rect x="9" y="5" width="1" height="2" fill={cat.accent} />
          <rect x="3" y="10" width="2" height="2" fill={cat.light} />
          <rect x="11" y="10" width="2" height="2" fill={cat.light} />
          <rect x="5" y="11" width="6" height="3" fill={cat.light} />
        </>
      )}

      {cat.pattern === "tuxedo" && (
        <>
          {/* Tuxedo white muzzle + chest V */}
          <rect x="6" y="9" width="4" height="4" fill={cat.light} />
          <rect x="5" y="10" width="6" height="3" fill={cat.light} />
          <rect x="7" y="8" width="2" height="2" fill={cat.light} />
        </>
      )}

      {cat.pattern === "calico" && (
        <>
          {/* Calico orange left patch + charcoal right patch */}
          <rect x="3" y="6" width="4" height="4" fill={cat.accent} />
          <rect x="4" y="5" width="3" height="2" fill={cat.accent} />
          <rect x="10" y="6" width="3" height="3" fill={cat.patch ?? "#2e323d"} />
          <rect x="9" y="5" width="3" height="2" fill={cat.patch ?? "#2e323d"} />
        </>
      )}

      {cat.pattern === "shaded" && (
        <>
          {/* Silver shaded soft top shading */}
          <rect x="4" y="5" width="8" height="2" fill={cat.accent} opacity="0.65" />
          <rect x="3" y="6" width="2" height="2" fill={cat.accent} opacity="0.45" />
          <rect x="11" y="6" width="2" height="2" fill={cat.accent} opacity="0.45" />
        </>
      )}

      {cat.pattern === "solid" && (
        <>
          <rect x="5" y="10" width="6" height="2" fill={cat.light} opacity="0.28" />
        </>
      )}

      {/* Eyes with bright pixel sparkle */}
      <rect x="5" y="8" width="2" height="2" fill={cat.eye} />
      <rect x="9" y="8" width="2" height="2" fill={cat.eye} />
      <rect x="5" y="8" width="1" height="1" fill="#ffffff" opacity="0.85" />
      <rect x="9" y="8" width="1" height="1" fill="#ffffff" opacity="0.85" />
      <rect x="6" y="9" width="1" height="1" fill={outline} opacity="0.85" />
      <rect x="10" y="9" width="1" height="1" fill={outline} opacity="0.85" />

      {/* Nose & tiny mouth */}
      <rect x="7" y="10" width="2" height="1" fill={cat.nose} />
      <rect x="6" y="11" width="1" height="1" fill={outline} opacity="0.55" />
      <rect x="9" y="11" width="1" height="1" fill={outline} opacity="0.55" />

      {/* Collar hint */}
      <rect x="5" y="14" width="6" height="1" fill={cat.collar} />
      <rect x="7" y="14" width="2" height="1" fill="#fde047" />
    </svg>
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
 * Full-body 24x24 pixel-art cat character sprite rendered in the Pixel Cat R&D
 * Studio, sharing exact breed coat/markings/eyes/collar with CatPortrait.
 */
export function CatCharacter({
  preset,
  pose = "idle",
  size = 64,
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
  const outline = "#12141a";
  const leftEar =
    cat.pattern === "calico" ||
    cat.pattern === "point" ||
    cat.pattern === "mask" ||
    cat.pattern === "tuxedo"
      ? cat.accent
      : cat.coat;
  const rightEar =
    cat.pattern === "calico"
      ? cat.patch ?? cat.accent
      : cat.pattern === "point" || cat.pattern === "mask" || cat.pattern === "tuxedo"
        ? cat.accent
        : cat.coat;
  const pawColor =
    cat.pattern === "mask" || cat.pattern === "point"
      ? cat.accent
      : cat.pattern === "tuxedo"
        ? cat.light
        : cat.coat;

  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      shapeRendering="crispEdges"
      className={className}
      aria-hidden="true"
      focusable="false"
    >
      {/* Ground shadow */}
      <rect x="6" y="21" width="12" height="2" fill="#000000" opacity="0.35" />

      {/* Tail */}
      <rect x="17" y="15" width="3" height="2" fill={outline} />
      <rect x="19" y="12" width="2" height="4" fill={outline} />
      <rect x="17" y="16" width="2" height="1" fill={cat.accent} />
      <rect x="19" y="13" width="1" height="3" fill={cat.coat} />
      <rect x="19" y="12" width="1" height="1" fill={cat.accent} />

      {/* Body outline */}
      <rect x="7" y="13" width="10" height="8" fill={outline} />
      <rect x="8" y="14" width="8" height="6" fill={cat.coat} />

      {/* Body markings */}
      {(cat.pattern === "tuxedo" ||
        cat.pattern === "stripes" ||
        cat.pattern === "mane") && (
        <rect x="10" y="15" width="4" height="5" fill={cat.light} />
      )}
      {cat.pattern === "calico" && (
        <>
          <rect x="8" y="15" width="3" height="3" fill={cat.accent} />
          <rect x="13" y="16" width="3" height="3" fill={cat.patch ?? "#2e323d"} />
        </>
      )}
      {cat.pattern === "stripes" && (
        <>
          <rect x="8" y="16" width="2" height="1" fill={cat.accent} />
          <rect x="14" y="16" width="2" height="1" fill={cat.accent} />
        </>
      )}

      {/* Collar & gold tag */}
      <rect x="8" y="13" width="8" height="2" fill={cat.collar} />
      <rect x="11" y="14" width="2" height="1" fill="#fde047" />

      {/* Paws (positioned by pose) */}
      {pose === "planning" ? (
        <>
          {/* Raised paw pointing to whiteboard */}
          <rect x="4" y="10" width="3" height="3" fill={outline} />
          <rect x="5" y="11" width="2" height="1" fill={pawColor} />
          <rect x="14" y="19" width="2" height="2" fill={pawColor} />
          <rect x="8" y="19" width="2" height="2" fill={pawColor} />
        </>
      ) : pose === "coding" || pose === "researching" ? (
        <>
          {/* Both paws forward typing */}
          <rect x="6" y="17" width="3" height="2" fill={pawColor} />
          <rect x="15" y="17" width="3" height="2" fill={pawColor} />
        </>
      ) : pose === "reviewing" ? (
        <>
          <rect x="8" y="19" width="2" height="2" fill={pawColor} />
          <rect x="14" y="19" width="2" height="2" fill={pawColor} />
          {/* Pixel magnifying glass */}
          <rect x="16" y="10" width="5" height="5" fill="#94a3b8" />
          <rect x="17" y="11" width="3" height="3" fill="#38bdf8" opacity="0.85" />
          <rect x="17" y="11" width="1" height="1" fill="#ffffff" />
          <rect x="15" y="15" width="2" height="2" fill="#475569" />
        </>
      ) : pose === "gate" ? (
        <>
          <rect x="8" y="19" width="2" height="2" fill={pawColor} />
          <rect x="14" y="19" width="2" height="2" fill={pawColor} />
          {/* Raised amber signal paddle */}
          <rect x="18" y="6" width="4" height="4" fill="#f59e0b" />
          <rect x="19" y="7" width="2" height="2" fill="#fef3c7" />
          <rect x="19" y="10" width="2" height="5" fill="#92400e" />
        </>
      ) : (
        <>
          <rect x="8" y="19" width="2" height="2" fill={pawColor} />
          <rect x="14" y="19" width="2" height="2" fill={pawColor} />
        </>
      )}

      {/* Head outline */}
      <rect x="6" y="3" width="3" height="4" fill={outline} />
      <rect x="15" y="3" width="3" height="4" fill={outline} />
      <rect x="6" y="5" width="12" height="8" fill={outline} />
      <rect x="7" y="4" width="10" height="9" fill={outline} />

      {/* Maine Coon ear tufts */}
      {cat.pattern === "mane" && (
        <>
          <rect x="6" y="2" width="1" height="1" fill={cat.accent} />
          <rect x="17" y="2" width="1" height="1" fill={cat.accent} />
        </>
      )}

      {/* Ears */}
      <rect x="7" y="4" width="2" height="2" fill={leftEar} />
      <rect x="15" y="4" width="2" height="2" fill={rightEar} />

      {/* Face base */}
      <rect x="7" y="6" width="10" height="6" fill={cat.coat} />
      <rect x="8" y="5" width="8" height="8" fill={cat.coat} />

      {/* Facial breed markings */}
      {cat.pattern === "mask" && (
        <rect x="8" y="7" width="8" height="5" fill={cat.accent} />
      )}
      {cat.pattern === "point" && (
        <>
          <rect x="7" y="6" width="3" height="3" fill={cat.accent} />
          <rect x="14" y="6" width="3" height="3" fill={cat.accent} />
          <rect x="10" y="8" width="4" height="5" fill={cat.light} />
        </>
      )}
      {cat.pattern === "stripes" && (
        <>
          <rect x="10" y="5" width="1" height="2" fill={cat.accent} />
          <rect x="13" y="5" width="1" height="2" fill={cat.accent} />
          <rect x="9" y="10" width="6" height="3" fill={cat.light} />
        </>
      )}
      {cat.pattern === "tuxedo" && (
        <rect x="9" y="9" width="6" height="4" fill={cat.light} />
      )}
      {cat.pattern === "calico" && (
        <>
          <rect x="7" y="6" width="4" height="3" fill={cat.accent} />
          <rect x="14" y="6" width="3" height="3" fill={cat.patch ?? "#2e323d"} />
        </>
      )}

      {/* Eyes */}
      {pose === "failed" ? (
        <>
          <rect x="9" y="8" width="2" height="1" fill="#f87171" />
          <rect x="13" y="8" width="2" height="1" fill="#f87171" />
        </>
      ) : (
        <>
          <rect x="9" y="8" width="2" height="2" fill={cat.eye} />
          <rect x="13" y="8" width="2" height="2" fill={cat.eye} />
          <rect x="9" y="8" width="1" height="1" fill="#ffffff" opacity="0.85" />
          <rect x="13" y="8" width="1" height="1" fill="#ffffff" opacity="0.85" />
        </>
      )}
      {/* Nose */}
      <rect x="11" y="10" width="2" height="1" fill={cat.nose} />
    </svg>
  );
}
