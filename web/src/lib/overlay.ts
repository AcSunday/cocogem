import { useLayoutEffect, type RefObject } from "react";

export function revealOption(list: HTMLElement, option: HTMLElement) {
  if (option.offsetTop < list.scrollTop) {
    list.scrollTop = option.offsetTop;
  } else if (
    option.offsetTop + option.offsetHeight >
    list.scrollTop + list.clientHeight
  ) {
    list.scrollTop = option.offsetTop + option.offsetHeight - list.clientHeight;
  }
}

/** Positions a fixed overlay against the visible viewport and its host dialog. */
export function useAnchoredOverlay(
  open: boolean,
  anchor: RefObject<HTMLElement | null>,
  overlay: RefObject<HTMLElement | null>,
  {
    minWidth = 180,
    limitHeight = 300,
    alignEnd = false,
    preferAbove = false,
  } = {},
) {
  useLayoutEffect(() => {
    const trigger = anchor.current;
    const popup = overlay.current;
    if (!open || !trigger || !popup) return;

    const viewport = window.visualViewport;
    const dialog = trigger.closest("dialog");
    const margin = 8;
    const gap = 6;
    const place = () => {
      const bounds = dialog?.getBoundingClientRect();
      const rect = trigger.getBoundingClientRect();
      const x = viewport?.offsetLeft ?? 0;
      const y = viewport?.offsetTop ?? 0;
      const left = Math.max(x, bounds?.left ?? x) + margin;
      const top = Math.max(y, bounds?.top ?? y) + margin;
      const right = Math.max(
        left,
        Math.min(
          x + (viewport?.width ?? window.innerWidth),
          bounds?.right ?? Infinity,
        ) - margin,
      );
      const bottom = Math.max(
        top,
        Math.min(
          y + (viewport?.height ?? window.innerHeight),
          bounds?.bottom ?? Infinity,
        ) - margin,
      );
      const width = Math.min(right - left, Math.max(minWidth, rect.width));
      popup.style.width = `${width}px`;

      const borders = popup.offsetHeight - popup.clientHeight;
      const desired = Math.min(limitHeight, popup.scrollHeight + borders);
      const upperEdge = Math.max(top, Math.min(rect.top - gap, bottom));
      const lowerEdge = Math.max(top, Math.min(rect.bottom + gap, bottom));
      const above = upperEdge - top;
      const below = bottom - lowerEdge;
      const up = preferAbove
        ? above >= desired || above > below
        : (below < desired || below === 0) && above > below;
      const height = Math.min(desired, up ? above : below);
      const anchorEdge = alignEnd ? rect.right - width : rect.left;
      popup.style.maxHeight = `${height}px`;
      popup.style.left = `${Math.max(left, Math.min(anchorEdge, right - width))}px`;
      popup.style.top = `${up ? upperEdge - height : lowerEdge}px`;
    };
    place();
    const scroll = (event: Event) => {
      if (event.target !== popup) place();
    };
    const resize =
      typeof ResizeObserver === "function"
        ? new ResizeObserver(place)
        : undefined;
    resize?.observe(trigger);
    resize?.observe(popup);
    if (dialog) resize?.observe(dialog);
    window.addEventListener("resize", place);
    window.addEventListener("scroll", scroll, true);
    viewport?.addEventListener("resize", place);
    viewport?.addEventListener("scroll", place);
    return () => {
      resize?.disconnect();
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", scroll, true);
      viewport?.removeEventListener("resize", place);
      viewport?.removeEventListener("scroll", place);
    };
  }, [open, anchor, overlay, minWidth, limitHeight, alignEnd, preferAbove]);
}
