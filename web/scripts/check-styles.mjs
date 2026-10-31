// Guardrail for the style architecture: `!important` is only acceptable in
// the prefers-reduced-motion reset, where overriding animations is the point.
// Everything else must win by specificity. Run as part of `npm run lint`.
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const stylesDir = path.join(
  path.dirname(fileURLToPath(import.meta.url)),
  "../src/styles",
);

const REDUCED_MOTION =
  /@media\s*\(prefers-reduced-motion:\s*reduce\)\s*\{(\s*\*\s*\{[^}]*\}\s*)+\}/g;

let failed = false;
for (const file of readdirSync(stylesDir)) {
  if (!file.endsWith(".css")) continue;
  const source = readFileSync(path.join(stylesDir, file), "utf8");
  const stripped = source
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(REDUCED_MOTION, "");
  const lines = stripped.split("\n");
  lines.forEach((line, index) => {
    if (line.includes("!important")) {
      console.error(`${file}:${index + 1}: !important outside the reduced-motion reset`);
      failed = true;
    }
  });
}
if (failed) process.exit(1);
