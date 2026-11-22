import { useId } from "react";

/**
 * Cocogem Prism Facets (Logo 01): three interlocking translucent geometric
 * ribbons (sapphire blue, warm terracotta coral, luminous teal) converging
 * into a unified diamond crystal.
 */
export function BrandLogo({
  size = 24,
  className,
}: {
  size?: number;
  className?: string;
}) {
  const uid = useId().replace(/:/g, "");
  const blueId = `cg-b-${uid}`;
  const coralId = `cg-c-${uid}`;
  const tealId = `cg-t-${uid}`;
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 64 64"
      fill="none"
      className={className}
      aria-hidden="true"
      focusable="false"
    >
      <defs>
        <linearGradient
          id={blueId}
          x1="8"
          y1="10"
          x2="36"
          y2="56"
          gradientUnits="userSpaceOnUse"
        >
          <stop offset="0%" stopColor="#4F8BFF" />
          <stop offset="100%" stopColor="#1D4ED8" />
        </linearGradient>
        <linearGradient
          id={coralId}
          x1="18"
          y1="10"
          x2="48"
          y2="56"
          gradientUnits="userSpaceOnUse"
        >
          <stop offset="0%" stopColor="#F28466" />
          <stop offset="100%" stopColor="#D95338" />
        </linearGradient>
        <linearGradient
          id={tealId}
          x1="28"
          y1="10"
          x2="56"
          y2="54"
          gradientUnits="userSpaceOnUse"
        >
          <stop offset="0%" stopColor="#5EEAD4" />
          <stop offset="100%" stopColor="#0D9488" />
        </linearGradient>
      </defs>
      <polygon
        points="18,10 38,10 26,24 32,56 8,24"
        fill={`url(#${blueId})`}
        opacity="0.96"
      />
      <polygon
        points="26,10 46,10 56,24 32,56 38,24"
        fill={`url(#${tealId})`}
        opacity="0.9"
      />
      <polygon
        points="18,10 46,10 32,56"
        fill={`url(#${coralId})`}
        opacity="0.82"
      />
      <polygon points="26,10 38,10 44,22 20,22" fill="#93C5FD" opacity="0.48" />
      <polygon points="20,22 44,22 32,56" fill="#FFFFFF" opacity="0.22" />
      <polygon points="32,22 44,22 32,56" fill="#2DD4BF" opacity="0.26" />
      <path
        d="M18 10h28l10 14-24 32L8 24 18 10Z"
        stroke="#FFFFFF"
        strokeOpacity="0.22"
        strokeWidth="1.5"
        strokeLinejoin="round"
      />
    </svg>
  );
}
