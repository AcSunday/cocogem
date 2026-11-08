import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  isConflict,
  type Settings,
  type SettingsInput,
} from "../api/client";

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
    const decided = fresh.execution_mode ?? "project_autonomous";
    if (decided !== (base.execution_mode ?? "project_autonomous")) return true;
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
  if (
    input.assignments !== undefined &&
    base?.assignments &&
    JSON.stringify(fresh.assignments ?? {}) !== JSON.stringify(base.assignments ?? {})
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
    value: "controlled",
    label: "受控",
    summary: "只做当前任务已允许的操作",
    description:
      "沿用成员模板和受控规则；项目内读写、命令与联网检索按现有授权执行。需要额外范围时，成员会在执行前申请。",
  },
  {
    value: "project_autonomous",
    label: "项目内自主",
    summary: "项目内自主，项目外申请",
    description:
      "项目内文件读写、测试、命令、联网检索和项目内依赖安装自主执行；项目外文件读写、系统软件安装、发布和部署在执行前申请。",
  },
  {
    value: "full_access",
    label: "完全访问",
    summary: "信任本机原生能力",
    description:
      "保持现有原生 CLI 放开行为，可按任务读写本机文件、执行命令并联网。这不是沙箱隔离；只读和审核角色限制仍然有效。",
  },
] as const;

export type ExecutionMode = (typeof executionModes)[number]["value"];

/** Defense in depth for a legacy or partial record. */
export function executionModeOf(settings?: Settings): ExecutionMode {
  const value = settings?.execution_mode;
  return value === "controlled" ||
    value === "full_access" ||
    value === "project_autonomous"
    ? value
    : "project_autonomous";
}

export const defaultBudgetPolicy = {
  auto_continuations: 20,
  parallel_branches: 4,
  writer_branches: 2,
  fix_rounds: 3,
  no_progress_failures: 2,
} as const;

export const budgetFields = [
  [
    "auto_continuations",
    "自动续办次数",
    "次",
    "首次用户启动后，所有自动后续 Run 共用额度。",
  ],
  ["parallel_branches", "同时推进的分支", "个", "排队和外部等待不占执行槽。"],
  [
    "writer_branches",
    "同时修改代码的分支",
    "个",
    "包含在总分支数内，不是额外执行槽。",
  ],
  [
    "fix_rounds",
    "同一问题自动修复轮数",
    "轮",
    "稳定问题 ID 关联修复与复验，不因改写描述重置。",
  ],
  [
    "no_progress_failures",
    "连续无进展失败次数",
    "次",
    "仅暂停对应分支，新证据和有效变更不算无进展。",
  ],
] as const;
