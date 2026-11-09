import { useMemo } from "react";

const color = (value: string | null, fallback: string) =>
  value && (/^#[0-9a-f]{6}$/i.test(value) || value === "white")
    ? value
    : fallback;

const number = (value: string | null, fallback: number) => {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : fallback;
};

/** Rebuild only the host chart's small SVG vocabulary; never inject source HTML. */
export function DataChart({ source }: { source: string }) {
  const chart = useMemo(() => {
    const document = new DOMParser().parseFromString(source, "image/svg+xml");
    const root = document.documentElement;
    if (root.localName !== "svg" || document.querySelector("parsererror")) {
      return null;
    }
    return {
      viewBox: root.getAttribute("viewBox") ?? "0 0 800 320",
      label: root.getAttribute("aria-label") ?? "CSV numeric values",
      children: Array.from(root.children).slice(0, 128).map((node, index) => {
        const key = `${node.localName}-${index}`;
        switch (node.localName) {
          case "title":
            return <title key={key}>{node.textContent}</title>;
          case "rect":
            return (
              <rect
                key={key}
                width={number(node.getAttribute("width"), 800)}
                height={number(node.getAttribute("height"), 320)}
                fill={color(node.getAttribute("fill"), "white")}
              />
            );
          case "line":
            return (
              <line
                key={key}
                x1={number(node.getAttribute("x1"), 0)}
                y1={number(node.getAttribute("y1"), 0)}
                x2={number(node.getAttribute("x2"), 0)}
                y2={number(node.getAttribute("y2"), 0)}
                stroke={color(node.getAttribute("stroke"), "#334155")}
              />
            );
          case "circle":
            return (
              <circle
                key={key}
                cx={number(node.getAttribute("cx"), 0)}
                cy={number(node.getAttribute("cy"), 0)}
                r={Math.min(20, Math.max(0, number(node.getAttribute("r"), 3)))}
                fill={color(node.getAttribute("fill"), "#2563eb")}
              >
                {Array.from(node.children)
                  .filter((child) => child.localName === "title")
                  .slice(0, 1)
                  .map((child) => (
                    <title key="title">{child.textContent}</title>
                  ))}
              </circle>
            );
          case "text":
            return (
              <text
                key={key}
                x={number(node.getAttribute("x"), 0)}
                y={number(node.getAttribute("y"), 0)}
                fontFamily="sans-serif"
                fontSize={Math.min(48, Math.max(1, number(node.getAttribute("font-size"), 14)))}
              >
                {node.textContent}
              </text>
            );
          default:
            return null;
        }
      }),
    };
  }, [source]);

  if (!chart) {
    return <pre tabIndex={0}>{source}</pre>;
  }
  return (
    <svg
      data-testid="data-chart"
      role="img"
      aria-label={chart.label}
      viewBox={chart.viewBox}
      width="100%"
      height="auto"
    >
      {chart.children}
    </svg>
  );
}
