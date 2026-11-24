import { useMemo } from "react";

/**
 * Hand-placed pixel sprites for the Cat Studio. Each row is one pixel row;
 * "." is transparent and every other letter is a PALETTE key. Runs of one
 * colour merge into a single rect so the SVG stays tiny and crisp at any scale.
 */
const PALETTE: Record<string, string> = {
  k: "#1b1d2b",
  w: "#f6f4ee",
  l: "#c9d6ea",
  g: "#8b9bb4",
  d: "#243045",
  T: "#0f766e",
  t: "#2dd4bf",
  u: "#a7f3e4",
  B: "#1e4fd6",
  b: "#5b9cff",
  c: "#cfe2ff",
  E: "#06775a",
  e: "#34d399",
  m: "#b3f5d8",
  R: "#b8432e",
  r: "#f48772",
  p: "#ffd2c6",
  Y: "#b45309",
  y: "#fbbf24",
  o: "#fff0b3",
  h: "#9a6233",
  H: "#5e391b",
  z: "#e3a92b",
  Z: "#9b6a12",
  x: "#ef4444",
  X: "#991b1b",
  s: "#ff8fb1",
  S: "#d9467a",
  v: "#8bd5ff",
};

const SPRITES = {
  loupe: [
    "..kkkkk.....",
    ".kzzzzzk....",
    "kzcwwcczk...",
    "kzcwcccczk..",
    "kzccccccZk..",
    "kzccccccZk..",
    ".kzcccczZk..",
    "..kZZZZZkhk.",
    "...kkkkkhhhk",
    "........khhk",
    ".........kk.",
  ],
  think: [
    "....kkkk........",
    "...kwwwwk.kkkk..",
    "..kwwwwwwkwwwwk.",
    ".kwwwwwwwwwwwwwk",
    "kwwwwwwwwwwwwwwk",
    "kwwddwwddwwddwwk",
    "kwwddwwddwwddwlk",
    ".kwwwwwwwwwwwwlk",
    "..kkwwwwwwwwllk.",
    "....kkkkkkkkkk..",
    ".kk.............",
    "kwlk............",
    ".kk.............",
  ],
  code: [
    ".TTTTTTTTTTTTT.",
    "TdddddddddddddT",
    "TdddddddddddddT",
    "TdddduddtuddddT",
    "TdddudddtdudddT",
    "TddudddtddduddT",
    "TdddudtdddudddT",
    "TddddutdduddddT",
    "TdddddddddddddT",
    "TdddddddddddddT",
    ".TTTTTTTTTTTTT.",
  ],
  doc: [
    "kkkkkk...",
    "kwwwwlk..",
    "kwwwwllk.",
    "kwggwkkkk",
    "kwwwwwwlk",
    "kwgggggwk",
    "kwwwwwwlk",
    "kwggggwwk",
    "kwwwwwwlk",
    "kwggwwwlk",
    "kllllllkk",
    "kkkkkkkk.",
  ],
  bulb: [
    "..kkkkk..",
    ".kooooyk.",
    "kooowoyyk",
    "koowooyyk",
    "kooooyyYk",
    "kooooyyYk",
    ".kooyyYk.",
    "..kyyYk..",
    "..kgggk..",
    "..klllk..",
    "..kgggk..",
    "...kkk...",
  ],
  alert: [
    "..kkkkk..",
    ".kxxxxxk.",
    "kxxwwwxXk",
    "kxxwwwxXk",
    "kxxwwwxXk",
    "kxxxwxxXk",
    "kxxxxxxXk",
    "kxxxwxxXk",
    ".kxxxxXk.",
    "..kkkkk..",
  ],
  check: [
    "..kkkkk..",
    ".keeeeek.",
    "keeeeewek",
    "keeeewwEk",
    "kewewweEk",
    "kewwweeEk",
    "keewweeEk",
    ".keeeeEk.",
    "..kkkkk..",
  ],
  key: [
    ".kkkk.......",
    "kyyyyk......",
    "kyokyykkkkk.",
    "kyokyyyyyyyk",
    "kyyyyykYkYkk",
    "kYYYYk.k.k..",
    ".kkkk.......",
  ],
  heart: [
    ".kk.kk.",
    "ksskssk",
    "kswsssk",
    "ksssssk",
    ".ksssk.",
    "..ksk..",
    "...k...",
  ],
  yarn: [
    "..kkkk..",
    ".kSsssk.",
    "kSssSsSk",
    "ksSSssSk",
    "kSsssSsk",
    "ksSssSSk",
    ".kSSssk.",
    "..kkkkSS",
  ],
  note: [
    "kkkkkkk",
    "kyyyyyk",
    "kyYYYyk",
    "kyyyyyk",
    "kyYYyyk",
    "kyyyyyk",
    "kkkkkkk",
  ],
  gemDiamond: [
    "...kkkkkk...",
    "..kcbbcbck..",
    ".kcbcbbcbbk.",
    "kkkkkkkkkkkk",
    "kcbbbcbbbBBk",
    ".kcbbbbbbBk.",
    "..kcbbbbBk..",
    "...kcbbBk...",
    "....kcBk....",
    ".....kk.....",
  ],
  gemEmerald: [
    "...kkkkkk...",
    "..kmmmmmek..",
    ".kmEEEEEEek.",
    "kmmEmeeeEeEk",
    "kmmEeweeEeEk",
    "kmmEeeeeEeEk",
    "kmmEeeeeEeEk",
    "kmmEeeeeEeEk",
    ".kmEEEEEEEk.",
    "..kEEEEEEk..",
    "...kkkkkk...",
  ],
  gemRuby: [
    ".....kk.....",
    "....kppk....",
    "....kprk....",
    "...kpprRk...",
    "..kpprrrRk..",
    ".kpwrrrrrRk.",
    ".kpwrrrrrRk.",
    "kprrrrrrrrRk",
    "kprrrrrrrRRk",
    ".kprrrrrRRk.",
    "..kRRrrRRk..",
    "...kkkkkk...",
  ],
  gemTopaz: [
    "....kkkk....",
    "...koyyYk...",
    "..kooyyyYk..",
    ".koowyyyYYk.",
    ".koowyyyYYk.",
    ".kooyyyyYYk.",
    ".kooyyyyYYk.",
    ".kooyyyyYYk.",
    ".koyyyyyYYk.",
    "..kyyyyyYk..",
    "...kyyyYk...",
    "....kkkk....",
  ],
} satisfies Record<string, readonly string[]>;

export type PixelSpriteName = keyof typeof SPRITES;

type Run = { x: number; y: number; w: number; fill: string };

const runCache = new Map<PixelSpriteName, Run[]>();

function runs(name: PixelSpriteName): Run[] {
  const cached = runCache.get(name);
  if (cached) return cached;
  const result: Run[] = [];
  SPRITES[name].forEach((row, y) => {
    let x = 0;
    while (x < row.length) {
      const key = row[x];
      if (key === ".") {
        x++;
        continue;
      }
      const start = x;
      while (x < row.length && row[x] === key) x++;
      result.push({ x: start, y, w: x - start, fill: PALETTE[key] });
    }
  });
  runCache.set(name, result);
  return result;
}

/** Crisp decorative pixel sprite; size the SVG with CSS (height/width). */
export function PixelSprite({
  name,
  className,
}: {
  name: PixelSpriteName;
  className?: string;
}) {
  const rows = SPRITES[name];
  const rects = useMemo(() => runs(name), [name]);
  return (
    <svg
      className={className}
      viewBox={`0 0 ${rows[0].length} ${rows.length}`}
      shapeRendering="crispEdges"
      aria-hidden="true"
      focusable="false"
    >
      {rects.map((rect) => (
        <rect
          key={`${rect.x}-${rect.y}`}
          x={rect.x}
          y={rect.y}
          width={rect.w}
          height={1}
          fill={rect.fill}
        />
      ))}
    </svg>
  );
}
