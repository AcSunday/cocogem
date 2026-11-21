import { useEffect, useMemo, useState } from "react";
import type { ToolPermissionRequest } from "../api/client";
import { t, useLanguage } from "../lib/i18n";
import { errorText } from "../lib/presentation";
import {
  ConversationGrants,
  type PermissionDecisionKind,
  submitPermissionDecision,
} from "./PermissionDecision";
import {
  budgetFields,
  defaultBudgetPolicy,
  executionModeOf,
  executionModes,
  type ExecutionMode,
  type SettingsState,
} from "../app/useGlobalSettings";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import settingsStyles from "../styles/settings.module.css";

const cx = scoped(base, controls, settingsStyles);

/** One-line user-facing boundary per mode; never shows epoch/grant internals. */
export const modeSummaries: Record<ExecutionMode, string> = {
  get workspace_autonomous() {
    return executionModes[0].summary;
  },
  get full_access() {
    return executionModes[1].summary;
  },
};

/**
 * Settings → 权限. This is an independent category beside 成员/协作/外观 and
 * reads and writes the same global value as the Composer: only
 * `execution_mode` is patched, so saving the permission never clears the
 * collaboration defaults and saving collaboration never resets the permission.
 */
export function PermissionSettings({
  settings,
  onExecutionMode,
  loading,
  error,
  threadID,
  threadTitle,
}: {
  settings?: SettingsState["settings"];
  onExecutionMode: (mode: ExecutionMode) => Promise<unknown>;
  loading?: boolean;
  error?: string;
  threadID?: string;
  threadTitle?: string;
}) {
  useLanguage();
  const selected = executionModeOf(settings);
  const [pending, setPending] = useState<ExecutionMode | "">("");
  const [saveError, setSaveError] = useState("");
  const [saved, setSaved] = useState("");
  useEffect(() => {
    setPending("");
  }, [selected]);
  const choose = async (mode: ExecutionMode) => {
    if (pending || mode === selected) return;
    setPending(mode);
    setSaveError("");
    setSaved("");
    try {
      await onExecutionMode(mode);
      setSaved(
        mode === "full_access"
          ? t(
              "已切换为完全访问；新执行使用原生能力。",
              "Switched to Full access; new runs will use native CLI capabilities.",
            )
          : t(
              "已切换为沙箱自主；工作台内零弹窗，越界才申请。",
              "Switched to Sandboxed; actions inside the workspace run without prompts.",
            ),
      );
    } catch (failure) {
      setSaveError(errorText(failure));
    } finally {
      setPending("");
    }
  };
  return (
    <section aria-labelledby="settings-permission">
      <h2 id="settings-permission">{t("权限", "Permissions")}</h2>
      <p className={cx("muted")}>
        {t(
          "权限是整个 Cocogem 共享的一份全局设置，不随工作空间保存。此处与输入框下方的权限选择读写同一个值，切换空间不会改变它。",
          "Permission mode is a global setting shared across Cocogem. This page and the selector below the composer read and write the same value; switching projects does not change it.",
        )}
      </p>
      {loading && !settings && (
        <p className={cx("muted")} role="status">
          {t("正在读取全局权限…", "Loading global permissions…")}
        </p>
      )}
      <div
        className={cx("permission-modes")}
        role="radiogroup"
        aria-label={t("执行权限模式", "Execution permission mode")}
      >
        {executionModes.map((option) => (
          <label
            key={option.value}
            className={cx("permission-mode")}
            data-selected={selected === option.value || undefined}
          >
            <input
              type="radio"
              name="global-execution-mode"
              value={option.value}
              checked={selected === option.value}
              disabled={!!pending || loading}
              onChange={() => void choose(option.value)}
            />
            <span>
              <strong>{option.label}</strong>
              <small>{option.description}</small>
            </span>
          </label>
        ))}
      </div>
      {!!pending && (
        <p className={cx("muted")} role="status">
          {t("正在保存权限…", "Saving permissions…")}
        </p>
      )}
      {(saveError || error) && (
        <p className={cx("error")} role="alert">
          {saveError || error}
        </p>
      )}
      {saved && !saveError && <p role="status">{saved}</p>}
      <p className={cx("muted")}>
        {t(
          "切换权限后，之前额外允许的操作和本会话持续授权都会失效，需要重新申请；任务预算、历史记录和已有任务不会重置，正在运行的步骤也不会自动重跑。",
          "After switching modes, previously approved one-off actions and session-wide permissions expire and must be requested again. Task budgets, history, and existing tasks are not reset, and running steps will not replay automatically.",
        )}
      </p>
      <section
        className={cx("settings-section")}
        aria-labelledby="permission-grants"
      >
        <h3 id="permission-grants">
          {t("会话持续授权", "Session-wide approvals")}
        </h3>
        <p className={cx("muted")}>
          {t(
            "你选择「本会话持续允许」的操作会列在这里；撤销后新的相同操作会再次停止或申请，已有历史记录不受影响。",
            "Operations approved with “Allow for this conversation” appear here. Revoking an item takes effect immediately for future actions while preserving history.",
          )}
        </p>
        <ConversationGrants
          threadID={threadID ?? ""}
          settings={settings}
          threadTitle={threadTitle}
        />
      </section>
    </section>
  );
}

/**
 * Settings → 协作 → 协作预算. Global defaults for future root tasks only; each
 * root task still keeps its own ledger and children/retries share their root.
 */
export type BudgetPolicyValue = {
  auto_continuations: number;
  parallel_branches: number;
  writer_branches: number;
  fix_rounds: number;
  no_progress_failures: number;
};

export function CollaborationBudget({
  settings,
  save,
}: {
  settings?: SettingsState["settings"];
  save: (policy: BudgetPolicyValue) => Promise<unknown>;
}) {
  useLanguage();
  const current = useMemo<BudgetPolicyValue>(
    () => ({
      auto_continuations:
        settings?.collaboration_budget?.auto_continuations ??
        defaultBudgetPolicy.auto_continuations,
      parallel_branches:
        settings?.collaboration_budget?.parallel_branches ??
        defaultBudgetPolicy.parallel_branches,
      writer_branches:
        settings?.collaboration_budget?.writer_branches ??
        defaultBudgetPolicy.writer_branches,
      fix_rounds:
        settings?.collaboration_budget?.fix_rounds ??
        defaultBudgetPolicy.fix_rounds,
      no_progress_failures:
        settings?.collaboration_budget?.no_progress_failures ??
        defaultBudgetPolicy.no_progress_failures,
    }),
    [settings?.collaboration_budget],
  );
  const [value, setValue] = useState(current);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  useEffect(() => setValue(current), [current]);
  const invalid =
    value.writer_branches > value.parallel_branches ||
    budgetFields.some(
      ([key]) => !Number.isInteger(value[key]) || value[key] < 1,
    );
  return (
    <form
      className={cx("settings-form")}
      onSubmit={(event) => {
        event.preventDefault();
        if (saving || invalid) return;
        setSaving(true);
        setError("");
        setSaved(false);
        void save(value)
          .then(() => setSaved(true))
          .catch((failure) => setError(errorText(failure)))
          .finally(() => setSaving(false));
      }}
    >
      <section className={cx("settings-section")}>
        <h3>{t("协作预算", "Collaboration budget")}</h3>
        <p>
          {t(
            "这些默认值仅对新建的主任务生效（子任务、后续修订与自动续办共用同一主任务额度）。已有任务的额度保持不变，切换权限也不会重置用量。",
            "These defaults apply only to newly created main tasks (subtasks, revisions, and auto-continuations share their main task's allowance). Existing tasks keep their current budget, and switching permission modes never resets usage.",
          )}
        </p>
        {budgetFields.map(([key, label, unit, description]) => (
          <label key={key}>
            {t(`${label}（${unit}）`, `${label} (${unit})`)}
            <input
              type="number"
              min={1}
              step={1}
              value={value[key]}
              aria-describedby={`budget-${key}`}
              onChange={(event) => {
                setSaved(false);
                setValue((prev) => ({
                  ...prev,
                  [key]: Number(event.target.value),
                }));
              }}
            />
            <small id={`budget-${key}`} className={cx("muted")}>
              {description}
            </small>
          </label>
        ))}
        {invalid && (
          <p className={cx("error")} role="alert">
            {t(
              "每项额度至少为 1，且同时修改代码的分支数不能超过同时推进的分支总数。",
              "Each budget value must be at least 1, and concurrent code-writing branches cannot exceed parallel branches.",
            )}
          </p>
        )}
        <button
          type="button"
          className={cx("ghost")}
          onClick={() => {
            setSaved(false);
            setValue({ ...defaultBudgetPolicy });
          }}
        >
          {t("恢复推荐默认值", "Restore recommended defaults")}
        </button>
        {error && (
          <p role="alert" className={cx("error")}>
            {error}
          </p>
        )}
        {saved && (
          <p role="status">
            {t(
              "协作预算已保存；仅对新建的主任务生效。",
              "Collaboration budget saved; applies to newly created main tasks.",
            )}
          </p>
        )}
        <div className={cx("form-actions")}>
          <button className={cx("primary")} disabled={saving || invalid}>
            {saving
              ? t("保存中…", "Saving…")
              : t("保存协作预算", "Save collaboration budget")}
          </button>
        </div>
      </section>
    </form>
  );
}

/** The revoke path for a still-visible permission card needs the same error text. */
export function decisionErrorText(failure: unknown) {
  return errorText(failure);
}

/**
 * Decide one pending request. Kept here so the App and the Settings page can
 * share the exact same HTTP semantics.
 */
export async function decidePending(
  request: ToolPermissionRequest,
  decision: PermissionDecisionKind,
) {
  return submitPermissionDecision(request, decision);
}
