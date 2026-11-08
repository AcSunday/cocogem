import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { Settings, SettingsInput } from "../api/client";
import {
  executionModeOf,
  SettingsConflictError,
  useGlobalSettings,
} from "./useGlobalSettings";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  localStorage.clear();
  sessionStorage.clear();
});

const settings = (overrides: Partial<Settings> = {}): Settings => ({
  revision: 3,
  default_coordinator_id: "coordinator",
  assignments: { engineering: { engineer: "writer" } },
  retention_days: 9,
  collaboration_budget: {
    auto_continuations: 20,
    parallel_branches: 4,
    writer_branches: 2,
    fix_rounds: 3,
    no_progress_failures: 2,
  },
  execution_mode: "project_autonomous",
  permission_epoch: 4,
  ...overrides,
});

/** A tiny server fake honouring If-Match and field-merge semantics. */
function server(initial: Settings) {
  let current = initial;
  const calls: { body: SettingsInput; ifMatch: string }[] = [];
  const fetch = vi.fn(async (path: string, options?: RequestInit) => {
    if (path !== "/api/v1/settings") return Response.json({ items: [] });
    if (!options?.method || options.method === "GET")
      return Response.json(current);
    const body = JSON.parse(String(options.body)) as SettingsInput;
    const ifMatch = new Headers(options.headers).get("If-Match") ?? "";
    calls.push({ body, ifMatch });
    if (Number(ifMatch) !== current.revision)
      return Response.json(
        { error: { message: "配置已更新，请刷新后重新编辑" } },
        { status: 409 },
      );
    // Field merge: only the supplied fields change, and a real permission
    // change advances the server-owned epoch.
    const next: Settings = { ...current };
    if (body.default_coordinator_id !== undefined && body.default_coordinator_id !== null)
      next.default_coordinator_id = body.default_coordinator_id;
    if (body.assignments !== undefined && body.assignments !== null)
      next.assignments = body.assignments;
    if (body.retention_days !== undefined && body.retention_days !== null)
      next.retention_days = body.retention_days;
    if (body.collaboration_budget) next.collaboration_budget = body.collaboration_budget;
    if (body.execution_mode && body.execution_mode !== current.execution_mode) {
      next.execution_mode = body.execution_mode;
      next.permission_epoch = (current.permission_epoch ?? 1) + 1;
    }
    next.revision = current.revision + 1;
    current = next;
    return Response.json(current);
  });
  return {
    fetch,
    calls,
    get current() {
      return current;
    },
    /** Simulates another window saving first. */
    external(overrides: Partial<Settings>) {
      current = { ...current, ...overrides, revision: current.revision + 1 };
    },
  };
}

beforeEach(() => {
  vi.stubGlobal(
    "BroadcastChannel",
    class {
      addEventListener() {}
      removeEventListener() {}
      postMessage() {}
      close() {}
    },
  );
});

it("patches only the permission field so collaboration defaults survive", async () => {
  const api = server(settings());
  vi.stubGlobal("fetch", api.fetch);
  const { result } = renderHook(() => useGlobalSettings());
  await waitFor(() => expect(result.current.settings).toBeDefined());
  await act(() => result.current.patch({ execution_mode: "full_access" }));
  expect(api.calls.map((call) => call.body)).toEqual([
    { execution_mode: "full_access" },
  ]);
  expect(api.calls[0].ifMatch).toBe("3");
  // Saving the permission did not clear anything else.
  expect(api.current.default_coordinator_id).toBe("coordinator");
  expect(api.current.assignments).toEqual({ engineering: { engineer: "writer" } });
  expect(api.current.retention_days).toBe(9);
  expect(api.current.collaboration_budget?.auto_continuations).toBe(20);
  // A changed boundary advances the server-owned epoch.
  expect(api.current.permission_epoch).toBe(5);
  expect(result.current.settings?.execution_mode).toBe("full_access");
});

it("saving the collaboration budget never overwrites the permission", async () => {
  const api = server(settings({ execution_mode: "controlled", permission_epoch: 7 }));
  vi.stubGlobal("fetch", api.fetch);
  const { result } = renderHook(() => useGlobalSettings());
  await waitFor(() => expect(result.current.settings).toBeDefined());
  await act(() =>
    result.current.patch({
      collaboration_budget: {
        auto_continuations: 11,
        parallel_branches: 3,
        writer_branches: 1,
        fix_rounds: 2,
        no_progress_failures: 1,
      },
    }),
  );
  expect(api.calls[0].body).not.toHaveProperty("execution_mode");
  expect(api.current.execution_mode).toBe("controlled");
  expect(api.current.permission_epoch).toBe(7);
  expect(api.current.default_coordinator_id).toBe("coordinator");
});

it("merges one narrow patch instead of resending a stale whole object", async () => {
  const api = server(settings());
  vi.stubGlobal("fetch", api.fetch);
  const { result } = renderHook(() => useGlobalSettings());
  await waitFor(() => expect(result.current.settings?.revision).toBe(3));
  // Another page saved retention and advanced the revision.
  api.external({ retention_days: 30 });
  // This page only changes the permission; the first attempt is rejected by
  // CAS, the hook re-reads and resends exactly its own field.
  await act(() => result.current.patch({ execution_mode: "controlled" }));
  expect(api.calls.map((call) => call.ifMatch)).toEqual(["3", "4"]);
  expect(api.calls.every((call) => Object.keys(call.body).length === 1)).toBe(true);
  expect(api.current.retention_days).toBe(30);
  expect(api.current.execution_mode).toBe("controlled");
  expect(result.current.settings?.revision).toBe(5);
});

it("keeps the previous value and surfaces the real error when saving fails", async () => {
  const api = server(settings());
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, options?: RequestInit) => {
      if (path === "/api/v1/settings" && options?.method === "PATCH")
        return Response.json(
          { error: { message: "权限代次不匹配" } },
          { status: 409 },
        );
      return api.fetch(path, options);
    }),
  );
  const { result } = renderHook(() => useGlobalSettings());
  await waitFor(() => expect(result.current.settings).toBeDefined());
  await expect(
    act(() => result.current.patch({ execution_mode: "full_access" })),
  ).rejects.toThrow();
  expect(result.current.settings?.execution_mode).toBe("project_autonomous");
});

it("re-reads from the server when another window broadcasts a newer revision", async () => {
  vi.useFakeTimers();
  const api = server(settings());
  vi.stubGlobal("fetch", api.fetch);
  const { result } = renderHook(() => useGlobalSettings());
  await act(async () => {
    await Promise.resolve();
  });
  expect(result.current.settings?.execution_mode).toBe("project_autonomous");
  // The other window stores the new permission server-side and announces it.
  api.external({ execution_mode: "full_access", permission_epoch: 5 });
  window.localStorage.setItem(
    "cocogem-settings",
    JSON.stringify({ revision: api.current.revision, at: Date.now() }),
  );
  act(() => {
    dispatchEvent(
      new StorageEvent("storage", {
        key: "cocogem-settings",
        newValue: localStorage.getItem("cocogem-settings"),
      }),
    );
  });
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  // The broadcast only prompts a re-read; the server value wins.
  expect(result.current.settings?.execution_mode).toBe("full_access");
});

it("converges within 2s while another window is visible", async () => {
  vi.useFakeTimers();
  const api = server(settings());
  vi.stubGlobal("fetch", api.fetch);
  const { result } = renderHook(() => useGlobalSettings());
  await act(async () => {
    await Promise.resolve();
  });
  expect(result.current.settings?.execution_mode).toBe("project_autonomous");
  // No broadcast at all: the bounded interval must still refresh.
  api.external({ execution_mode: "controlled", permission_epoch: 5 });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(2000);
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(result.current.settings?.execution_mode).toBe("controlled");
});

it("defensively defaults an older record to project_autonomous", () => {
  expect(executionModeOf(undefined)).toBe("project_autonomous");
  expect(executionModeOf({ revision: 1, default_coordinator_id: "", assignments: {} }))
    .toBe("project_autonomous");
  expect(executionModeOf(settings({ execution_mode: "controlled" }))).toBe(
    "controlled",
  );
  expect(executionModeOf(settings({ execution_mode: "legacy_guess" }))).toBe(
    "project_autonomous",
  );
});

it("a stale GET that resolves after a save never rolls the mode backward", async () => {
  // A GET whose response the test controls explicitly: `hold` is the promise the
  // hook is waiting on for the pre-save read.
  let releaseRead!: () => void;
  const held = new Promise<void>((resolve) => (releaseRead = resolve));
  let current = settings();
  const fetch = vi.fn(async (path: string, options?: RequestInit) => {
    if (path !== "/api/v1/settings") return Response.json({ items: [] });
    if (!options?.method || options.method === "GET") {
      const snapshot = current;
      await held;
      return Response.json(snapshot);
    }
    const body = JSON.parse(String(options.body)) as SettingsInput;
    const next: Settings = {
      ...current,
      execution_mode: body.execution_mode ?? current.execution_mode,
      revision: current.revision + 1,
    };
    current = next;
    return Response.json(current);
  });
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() => useGlobalSettings());
  expect(result.current.settings).toBeUndefined();
  // Save while the old snapshot (project_autonomous, r=3) is still held.
  await act(async () => {
    await result.current.patch({ execution_mode: "full_access" });
  });
  expect(result.current.settings?.execution_mode).toBe("full_access");
  await act(async () => {
    releaseRead();
    await Promise.resolve();
    await Promise.resolve();
  });
  // The delayed read must not regress the mode back to project_autonomous.
  expect(result.current.settings?.execution_mode).toBe("full_access");
  expect(result.current.settings?.revision).toBe(4);
});

it("a held PATCH that resolves after a newer save never rolls the state back", async () => {
  // A PATCH whose response the test holds: the save keeps If-Match r3 -> the
  // server answers r4, but another window reaches r5 first.
  let releasePatch!: () => void;
  const held = new Promise<void>((resolve) => (releasePatch = resolve));
  const current = settings(); // r3
  const fetch = vi.fn(async (path: string, options?: RequestInit) => {
    if (path !== "/api/v1/settings") return Response.json({ items: [] });
    if (!options?.method || options.method === "GET")
      return Response.json(current);
    const body = JSON.parse(String(options.body)) as SettingsInput;
    const ifMatch = Number(new Headers(options.headers).get("If-Match"));
    if (ifMatch !== current.revision)
      return Response.json({ error: { message: "revision" } }, { status: 409 });
    // This save produces r4, but its answer is delayed.
    const committed: Settings = {
      ...current,
      execution_mode: body.execution_mode ?? current.execution_mode,
      revision: ifMatch + 1,
    };
    await held;
    return Response.json(committed);
  });
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() => useGlobalSettings());
  await waitFor(() => expect(result.current.settings?.revision).toBe(3));

  // Start the save; its response is held at r4.
  let saving!: Promise<Settings>;
  await act(async () => {
    saving = result.current.patch({ execution_mode: "full_access" });
    await Promise.resolve();
  });
  // Another window saves r5 (controlled) and this page applies it first.
  await act(async () => {
    result.current.apply({ ...settings(), execution_mode: "controlled", revision: 5 });
    await Promise.resolve();
  });
  expect(result.current.settings?.revision).toBe(5);
  // Now the held r4 response resolves.
  await act(async () => {
    releasePatch();
    await saving;
  });
  // The stale save result must not regress the mode to its own r4 answer.
  expect(result.current.settings?.revision).toBe(5);
  expect(result.current.settings?.execution_mode).toBe("controlled");
});

it("does not silently overwrite a same-field change from another window", async () => {
  vi.unstubAllGlobals();
  vi.stubGlobal(
    "BroadcastChannel",
    class {
      addEventListener() {}
      removeEventListener() {}
      postMessage() {}
      close() {}
    },
  );
  const api = server(settings());
  vi.stubGlobal("fetch", api.fetch);
  const { result } = renderHook(() => useGlobalSettings());
  await waitFor(() => expect(result.current.settings?.revision).toBe(3));
  // Another window picks controlled before this page saves its own choice.
  api.external({ execution_mode: "controlled", permission_epoch: 5 });
  await expect(
    act(() => result.current.patch({ execution_mode: "full_access" })),
  ).rejects.toBeInstanceOf(SettingsConflictError);
  // Exactly one PATCH was attempted: no silent retry over the newer choice.
  expect(api.calls).toHaveLength(1);
  // The state re-read reflects the other window's decision.
  await act(async () => {
    await result.current.refresh();
  });
  expect(api.current.execution_mode).toBe("controlled");
  expect(result.current.settings?.execution_mode).toBe("controlled");
});

it("still merges a narrow non-conflicting patch after a CAS conflict", async () => {
  vi.unstubAllGlobals();
  vi.stubGlobal(
    "BroadcastChannel",
    class {
      addEventListener() {}
      removeEventListener() {}
      postMessage() {}
      close() {}
    },
  );
  const api = server(settings());
  vi.stubGlobal("fetch", api.fetch);
  const { result } = renderHook(() => useGlobalSettings());
  await waitFor(() => expect(result.current.settings?.revision).toBe(3));
  // Another window changed the permission (a different field than this patch).
  api.external({ execution_mode: "full_access", permission_epoch: 5 });
  await act(() =>
    result.current.patch({
      collaboration_budget: {
        auto_continuations: 7,
        parallel_branches: 4,
        writer_branches: 2,
        fix_rounds: 3,
        no_progress_failures: 2,
      },
    }),
  );
  expect(api.calls.map((call) => call.ifMatch)).toEqual(["3", "4"]);
  expect(api.current.execution_mode).toBe("full_access");
  expect(api.current.collaboration_budget?.auto_continuations).toBe(7);
});
