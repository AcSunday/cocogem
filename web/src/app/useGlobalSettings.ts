import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  isConflict,
  type Settings,
  type SettingsInput,
} from "../api/client";
import { t } from "../lib/i18n";

/**
 * The global permission choice is one server-owned value. Composer and
 * Settings → 权限 both read `Settings.execution_mode` from this hook and both
 * write it through a field-merge `PATCH /api/v1/settings` with `If-Match`.
 *
 * Cross-window sync never trusts a broadcast as the truth: it only says "the
 * revision moved, read again". The server stays authoritative, so a late
 * broadcast can never resurrect an older permission value.
 */
export const SETTINGS_CHANNEL = "cocogem-settings";

export type SettingsState = {
  settings?: Settings;
  /** True until the first read lands (or fails). */
  loading: boolean;
  /** Last real error from a background re-read or a save. */
  error: string;
  /** Replace the whole record, e.g. after a settings PATCH returned one. */
  apply: (value: Settings) => void;
  /** Read the server value again. Rejects only on a hard failure. */
  refresh: () => Promise<Settings | undefined>;
  /**
   * Field-merge save against the freshest revision this hook knows. Only the
   * given fields are sent, so saving collaboration never overwrites the
   * permission and saving the permission never clears collaboration defaults.
   * A concurrent editor wins: on 409 the hook re-reads and retries once with the
   * newest revision because the patch carries no other page's fields.
   */
  patch: (
    input: SettingsInput,
    options?: { retry?: boolean },
  ) => Promise<Settings>;
};

/** A real conflict the caller must resolve by re-reading, not by retrying. */
export class SettingsConflictError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "SettingsConflictError";
  }
}

/** True when the fields this patch writes already differ from the base. */
function sameFieldChanged(
  input: SettingsInput,
  fresh: Settings,
  base: Settings | undefined,
): boolean {
  if (input.execution_mode && base) {
    const decided = fresh.execution_mode ?? "workspace_autonomous";
    if (decided !== (base.execution_mode ?? "workspace_autonomous")) return true;
  }
  if (
    input.collaboration_budget &&
    base?.collaboration_budget &&
    fresh.collaboration_budget &&
    JSON.stringify(fresh.collaboration_budget) !==
      JSON.stringify(base.collaboration_budget)
  )
    return true;
  if (input.retention_days !== undefined && base && fresh.retention_days !== base.retention_days)
    return true;
  if (
    input.default_coordinator_id !== undefined &&
    base &&
    (fresh.default_coordinator_id ?? "") !== (base.default_coordinator_id ?? "")
  )
    return true;
  return false;
}

function broadcast(revision: number) {
  try {
    window.localStorage.setItem(
      SETTINGS_CHANNEL,
      JSON.stringify({ revision, at: Date.now() }),
    );
  } catch {
    // Quota/privacy failures fall back to focus/visibility re-reads.
  }
  try {
    const channel =
      typeof BroadcastChannel === "function"
        ? new BroadcastChannel(SETTINGS_CHANNEL)
        : undefined;
    if (!channel) return;
    channel.postMessage({ revision });
    channel.close();
  } catch {
    // A browser without BroadcastChannel still gets the storage event.
  }
}

export function useGlobalSettings(
  initial?: Settings,
  options: { enabled?: boolean } = {},
): SettingsState {
  const enabled = options.enabled !== false;
  const [settings, setSettings] = useState<Settings | undefined>(initial);
  const [loading, setLoading] = useState(!initial);
  const [error, setError] = useState("");
  const latest = useRef<Settings | undefined>(initial);
  const generation = useRef(0);
  const saving = useRef(0);
  // Every write goes through the monotonic guard, including a save result: a
  // PATCH that was held while another window saved a higher revision must not
  // roll the visible state back to its older response.
  const set = useCallback((value: Settings): Settings => {
    // An older revision never replaces a newer one; otherwise (equal or newer)
    // the incoming record becomes the visible state.
    if (latest.current && value.revision < latest.current.revision)
      return latest.current;
    latest.current = value;
    setSettings(value);
    return value;
  }, []);

  const refreshRef = useRef<() => Promise<Settings | undefined>>(async () => undefined);
  const refresh = useCallback(async () => {
    const mine = ++generation.current;
    try {
      const value = await api<Settings>("/api/v1/settings");
      if (mine !== generation.current) return latest.current;
      // Monotonic revision, regardless of whether a save is in flight: any
      // read older than the current state is discarded.
      if (latest.current && value.revision <= latest.current.revision) {
        setLoading(false);
        return latest.current;
      }
      setError("");
      set(value);
      return value;
    } catch (failure) {
      if (mine === generation.current) {
        setError(failure instanceof Error ? failure.message : String(failure));
      }
      throw failure;
    } finally {
      if (mine === generation.current) setLoading(false);
    }
  }, [set]);
  refreshRef.current = refresh;

  const patch = useCallback(
    async (input: SettingsInput, options: { retry?: boolean } = {}) => {
      // Writing invalidates every read that started before it, so a stale GET
      // can never land after the save and roll the state backward.
      generation.current++;
      const attempt = async (
        record: Settings | undefined,
      ): Promise<Settings> => {
        saving.current++;
        try {
          const saved = await api<Settings>(
            "/api/v1/settings",
            "PATCH",
            input,
            {
              "If-Match": String(record?.revision ?? 0),
            },
          );
          // The response only replaces state if it is not older than what is
          // already shown; the caller still receives the server's own result.
          generation.current++;
          set(saved);
          broadcast(saved.revision);
          return saved;
        } finally {
          saving.current = Math.max(0, saving.current - 1);
        }
      };
      const base = latest.current;
      try {
        return await attempt(base);
      } catch (failure) {
        if (!isConflict(failure) || options.retry === false) throw failure;
        // CAS rejected this patch. Re-read to see what actually changed.
        const fresh = await refreshRef.current();
        const next = fresh ?? latest.current;
        if (!next) throw failure;
        // Same-field conflict: another window changed the field this patch
        // would write. Retrying would silently overwrite a newer user choice,
        // so surface it and let the caller decide (the UI re-reads and stops).
        if (sameFieldChanged(input, next, base)) {
          throw new SettingsConflictError(
            "other window changed the same setting; reload to keep its newer choice",
          );
        }
        // Only non-conflicting fields (roles, retention, budget) are merged.
        return attempt(next);
      }
    },
    [set],
  );

  const apply = useCallback(
    (value: Settings) => {
      // Monotonic: a lower revision (a late read or a delayed caller) never
      // replaces a newer saved state.
      if (latest.current && value.revision <= latest.current.revision) return;
      generation.current++;
      set(value);
    },
    [set],
  );

  useEffect(() => {
    if (!enabled) return;
    let canceled = false;
    void refresh().catch(() => {
      if (!canceled) setLoading(false);
    });
    return () => {
      canceled = true;
    };
  }, [enabled, refresh]);

  useEffect(() => {
    if (!enabled) return;
    const maybe = (revision: number | undefined) => {
      const current = latest.current?.revision;
      if (revision && current !== undefined && revision <= current) return;
      void refresh().catch(() => {});
    };
    const onStorage = (event: StorageEvent) => {
      if (event.key !== SETTINGS_CHANNEL || !event.newValue) return;
      try {
        const value = JSON.parse(event.newValue) as { revision?: number };
        maybe(typeof value.revision === "number" ? value.revision : undefined);
      } catch {
        void refresh().catch(() => {});
      }
    };
    const onMessage = (event: MessageEvent) => {
      const revision =
        event.data && typeof event.data === "object"
          ? (event.data as { revision?: number }).revision
          : undefined;
      maybe(typeof revision === "number" ? revision : undefined);
    };
    // Both transports are used on purpose: the storage event fires in other
    // windows without support for BroadcastChannel, and the channel is the
    // faster path where it exists. Duplicate reads coalesce in the generation
    // guard above.
    let channel: BroadcastChannel | undefined;
    try {
      channel =
        typeof BroadcastChannel === "function"
          ? new BroadcastChannel(SETTINGS_CHANNEL)
          : undefined;
      channel?.addEventListener("message", onMessage as EventListener);
    } catch {
      channel = undefined;
    }
    // The guaranteed path: any focus or visible state re-reads the server, so
    // two visible windows converge within the bounded interval even when
    // neither broadcast transport fires.
    const onFocus = () => maybe(undefined);
    const onVisibility = () => {
      if (document.visibilityState === "visible") maybe(undefined);
    };
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") maybe(undefined);
    }, 2000);
    window.addEventListener("storage", onStorage);
    window.addEventListener("focus", onFocus);
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener("storage", onStorage);
      window.removeEventListener("focus", onFocus);
      document.removeEventListener("visibilitychange", onVisibility);
      channel?.removeEventListener("message", onMessage as EventListener);
      channel?.close();
    };
  }, [enabled, refresh]);

  return { settings, loading, error, apply, refresh, patch };
}

export const executionModes = [
  {
    value: "workspace_autonomous",
    get label() {
      return t("沙箱自主", "Sandboxed");
    },
    get summary() {
      return t(
        "工作台内零弹窗，越界才申请",
        "Auto-run inside workspace; prompt only outside",
      );
    },
    get description() {
      return t(
        "在当前项目隔离目录、临时目录（/tmp）与本地环境内自动读写文件、运行测试和命令，无需反复确认。钥匙串与私钥等高敏凭据默认受保护；访问项目外目录、推送代码（git push）或发布到外部时再向你申请。",
        "Automatically read/write files, run tests, and execute commands inside the isolated project worktree, /tmp, and local toolchain without repeated prompts. Sensitive credentials (keychains, private keys) are blocked by default; prompts appear only when accessing outside directories, running git push, or publishing externally.",
      );
    },
  },
  {
    value: "full_access",
    get label() {
      return t("完全访问", "Full access");
    },
    get summary() {
      return t(
        "信任本机原生能力",
        "Trust native local CLI capabilities",
      );
    },
    get description() {
      return t(
        "直接使用本机 CLI 的完整能力，可按任务需要读写本机任意文件、执行命令并访问网络（无沙箱隔离）。「仅规划」与独立复核的只读限制依然生效。",
        "Uses the local CLI's full capabilities to read/write local files, run commands, and access the network without sandbox isolation. Read-only limits for Plan-only mode and independent reviewers still apply.",
      );
    },
  },
] as const;

export type ExecutionMode = (typeof executionModes)[number]["value"];

/** Defense in depth for a legacy or partial record. */
export function executionModeOf(settings?: Settings): ExecutionMode {
  // A legacy controlled/project_autonomous record is the same autonomous tier
  // today; it is never silently promoted to full access.
  return settings?.execution_mode === "full_access"
    ? "full_access"
    : "workspace_autonomous";
}

export const defaultBudgetPolicy = {
  auto_continuations: 20,
  parallel_branches: 4,
  writer_branches: 2,
  fix_rounds: 3,
  no_progress_failures: 2,
} as const;

type BudgetFieldKey = keyof typeof defaultBudgetPolicy;

function defineBudgetField<K extends BudgetFieldKey>(
  key: K,
  label: () => string,
  unit: () => string,
  description: () => string,
): readonly [K, string, string, string] {
  const tuple = [key, "", "", ""] as [K, string, string, string];
  Object.defineProperties(tuple, {
    1: { get: label, enumerable: true },
    2: { get: unit, enumerable: true },
    3: { get: description, enumerable: true },
  });
  return tuple;
}

export const budgetFields = [
  defineBudgetField(
    "auto_continuations",
    () => t("自动续办次数", "Auto-continuations"),
    () => t("次", "runs"),
    () =>
      t(
        "启动后自动继续推进的次数上限（与子任务共用）。",
        "Maximum automatic follow-up runs after you start a task (shared with subtasks).",
      ),
  ),
  defineBudgetField(
    "parallel_branches",
    () => t("同时推进的分支", "Parallel branches"),
    () => t("个", "branches"),
    () =>
      t(
        "最多同时并行推进的分支数（排队和等待不占用名额）。",
        "Maximum branches running in parallel (queued or waiting tasks do not use a slot).",
      ),
  ),
  defineBudgetField(
    "writer_branches",
    () => t("同时修改代码的分支", "Concurrent code-writing branches"),
    () => t("个", "branches"),
    () =>
      t(
        "可同时修改代码的分支数（包含在并行分支总数内）。",
        "Branches allowed to modify files at the same time (counted within parallel branches).",
      ),
  ),
  defineBudgetField(
    "fix_rounds",
    () => t("同一问题自动修复轮数", "Auto-fix rounds per issue"),
    () => t("轮", "rounds"),
    () =>
      t(
        "同一问题最多自动尝试修复并复验的轮数。",
        "Maximum automatic fix-and-recheck rounds for the same issue.",
      ),
  ),
  defineBudgetField(
    "no_progress_failures",
    () => t("连续无进展失败次数", "Consecutive no-progress failures"),
    () => t("次", "times"),
    () =>
      t(
        "单个分支连续失败且无新进展时自动暂停的阈值。",
        "Pauses a branch when it fails this many times in a row without new progress.",
      ),
  ),
] as const;
