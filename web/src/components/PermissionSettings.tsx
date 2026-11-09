import { useEffect, useMemo, useState } from "react";
import type { ToolPermissionRequest } from "../api/client";
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
  controlled: executionModes[0].summary,
  project_autonomous: executionModes[1].summary,
  full_access: executionModes[2].summary,
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
        mode === "controlled"
          ? "已切换为受控；后续派发和工具准入使用新边界。"
          : mode === "full_access"
            ? "已切换为完全访问；新执行使用原生能力。"
            : "已切换为项目内自主；新执行在项目内自主、项目外申请。",
      );
    } catch (failure) {
      setSaveError(errorText(failure));
    } finally {
      setPending("");
    }
  };
  return (
    <section aria-labelledby="settings-permission">
      <h2 id="settings-permission">权限</h2>
      <p className={cx("muted")}>
        权限是整个 Cocogem
        共享的一份全局设置，不随工作空间保存。此处与输入框下方的权限选择读写同一个值，切换空间不会改变它。
      </p>
      {loading && !settings && (
        <p className={cx("muted")} role="status">
          正在读取全局权限…
        </p>
      )}
      <div
        className={cx("permission-modes")}
        role="radiogroup"
        aria-label="执行权限模式"
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
          正在保存权限…
        </p>
      )}
      {(saveError || error) && (
        <p className={cx("error")} role="alert">
          {saveError || error}
        </p>
      )}
      {saved && !saveError && <p role="status">{saved}</p>}
      <p className={cx("muted")}>
        切换后，之前额外允许的操作和本会话持续授权都会失效，需要重新申请；任务预算、历史记录和已有任务不会重置。已开始的原生执行不会自动重放。
      </p>
      <section
        className={cx("settings-section")}
        aria-labelledby="permission-grants"
      >
        <h3 id="permission-grants">会话持续授权</h3>
        <p className={cx("muted")}>
          “本会话持续允许”的记录在这里撤销；撤销后新执行立即被拒绝，历史记录保留。
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
        <h3>协作预算</h3>
        <p>
          这些默认值只用于以后新建的根任务，每个根任务分别累计；子任务、修订和自动续办共享所属根账本。现有任务保持原预算，切换权限也不会重置用量。
        </p>
        {budgetFields.map(([key, label, unit, description]) => (
          <label key={key}>
            {label}（{unit}）
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
            每个预算至少为 1；同时修改代码的分支不能大于同时推进的分支。
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
          恢复推荐默认值
        </button>
        {error && (
          <p role="alert" className={cx("error")}>
            {error}
          </p>
        )}
        {saved && <p role="status">协作预算已保存；只影响以后创建的根任务。</p>}
        <div className={cx("form-actions")}>
          <button className={cx("primary")} disabled={saving || invalid}>
            {saving ? "保存中…" : "保存协作预算"}
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
