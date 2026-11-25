import { useEffect, useRef } from "react";
import { APIError } from "../api/client";
import { errorText } from "../lib/presentation";

/**
 * A rejection nobody handled is a bug, but two kinds are expected noise: a
 * canceled request (AbortError) and an expired login (401), which the app
 * already turns into its own sign-in flow.
 */
export function shouldReportRejection(reason: unknown): boolean {
  if (
    typeof reason === "object" &&
    reason !== null &&
    (reason as { name?: unknown }).name === "AbortError"
  )
    return false;
  if (reason instanceof APIError && reason.status === 401) return false;
  return true;
}

/**
 * Surfaces an unhandled promise rejection through the app's normal error
 * notice instead of failing silently. The event is not prevented, so the
 * browser console still records the original reason.
 */
export function useUnhandledRejection(report: (message: string) => void) {
  const latest = useRef(report);
  latest.current = report;
  useEffect(() => {
    const onRejection = (event: PromiseRejectionEvent) => {
      if (!shouldReportRejection(event.reason)) return;
      latest.current(errorText(event.reason));
    };
    window.addEventListener("unhandledrejection", onRejection);
    return () => window.removeEventListener("unhandledrejection", onRejection);
  }, []);
}
