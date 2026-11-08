import { useRef, useState, type FormEvent } from "react";
import { errorText } from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import acceptance from "../styles/task-acceptance.module.css";
import { Modal } from "./Modal";

const cx = scoped(base, controls, acceptance);

export type AcceptanceCandidate = {
  ref: string;
  label: string;
};

export type AcceptanceCriterion = {
  criterion_id: string;
  statement: string;
  required: boolean;
};

export type AcceptanceResult = {
  criterion_id: string;
  result: string;
  evidence: string[];
  limitations: string[];
};

export type AcceptanceSubmission = {
  candidate_refs: string[];
  results: AcceptanceResult[];
};

const judgementOptions = [
  { value: "inconclusive", label: "无法确认" },
  { value: "passed", label: "通过" },
  { value: "failed", label: "未通过" },
  { value: "not_run", label: "未验证" },
  { value: "not_applicable", label: "不适用" },
];

function isOutcome(criterion: AcceptanceCriterion, goal: string) {
  return (
    criterion.criterion_id === "outcome" ||
    criterion.statement.trim() === goal.trim()
  );
}

/**
 * Human acceptance is a dedicated dialog: closing or pressing Escape never
 * saves anything, a candidate is always an exact artifact version or exact
 * successful Delivery message, and a failed submission keeps every input.
 */
export function TaskAcceptanceDialog({
  goal,
  criteria,
  candidates,
  expired = false,
  refresh,
  close,
  submit,
}: {
  goal: string;
  criteria: AcceptanceCriterion[];
  candidates: AcceptanceCandidate[];
  expired?: boolean;
  refresh?: () => void;
  close: () => void;
  submit: (submission: AcceptanceSubmission) => Promise<void>;
}) {
  const initialCandidate = candidates[0]?.ref ?? "";
  const [candidate, setCandidate] = useState(initialCandidate);
  const [results, setResults] = useState<Record<string, string>>({});
  const [evidence, setEvidence] = useState<Record<string, string[]>>(() =>
    criteria.reduce<Record<string, string[]>>((current, criterion) => {
      if (initialCandidate && isOutcome(criterion, goal))
        current[criterion.criterion_id] = [initialCandidate];
      return current;
    }, {}),
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submitting = useRef(false);
  // Modal's top X, Escape and backdrop all route through this close. Once a
  // save starts, no dismissal may unmount the instance and allow a re-submit.
  const requestClose = () => {
    if (submitting.current) return;
    close();
  };

  const criterionText = (statement: string) =>
    statement.trim() === goal.trim() ? "交付满足上述任务目标" : statement;
  const resultFor = (criterion: AcceptanceCriterion) =>
    results[criterion.criterion_id] || "inconclusive";
  const evidenceFor = (criterion: AcceptanceCriterion) =>
    evidence[criterion.criterion_id] ?? [];
  const missingEvidence = criteria.some(
    (criterion) =>
      resultFor(criterion) === "passed" && evidenceFor(criterion).length === 0,
  );
  const unresolved = criteria.filter(
    (criterion) => criterion.required && resultFor(criterion) !== "passed",
  ).length;

  const chooseCandidate = (value: string) => {
    setCandidate(value);
    setEvidence((current) => {
      const next = { ...current };
      for (const criterion of criteria) {
        if (isOutcome(criterion, goal)) next[criterion.criterion_id] = [value];
      }
      return next;
    });
  };

  const onFormSubmit = (event: FormEvent) => {
    event.preventDefault();
    if (!candidate || missingEvidence || expired || submitting.current) return;
    submitting.current = true;
    setBusy(true);
    setError("");
    void submit({
      candidate_refs: [candidate],
      results: criteria.map((criterion) => ({
        criterion_id: criterion.criterion_id,
        result: resultFor(criterion),
        evidence: evidenceFor(criterion),
        limitations: [],
      })),
    })
      .then(close)
      .catch((failure) => {
        // A failed save keeps the exact candidate, judgements and evidence.
        setError(errorText(failure));
      })
      .finally(() => {
        submitting.current = false;
        setBusy(false);
      });
  };

  return (
    <Modal title="人工验收" close={requestClose} wide>
      <form
        className={cx("acceptance-form")}
        aria-label="人工验收表单"
        onSubmit={onFormSubmit}
      >
        {expired && (
          <div className={cx("acceptance-expired")} role="alert">
            <strong>验收依据已更新</strong>
            <p>
              任务目标、版本或可验收交付在弹窗打开后发生变化。旧判断不会套用到新版本；请重新核对。
            </p>
            {refresh && (
              <button type="button" onClick={refresh} disabled={busy}>
                重新核对
              </button>
            )}
          </div>
        )}
        <p className={cx("acceptance-intro")}>
          选择一个确切成果版本或本任务成功执行的交付回复，再逐项判断。关闭不会保存，也不会默认通过。
        </p>

        <fieldset className={cx("acceptance-candidates")}>
          <legend>候选成果</legend>
          {candidates.map((item) => (
            <label
              className={cx(
                "acceptance-candidate",
                item.ref === candidate && "selected",
              )}
              key={item.ref}
            >
              <input
                type="radio"
                name="acceptance-candidate"
                value={item.ref}
                checked={item.ref === candidate}
                onChange={() => chooseCandidate(item.ref)}
              />
              <span>{item.label}</span>
            </label>
          ))}
        </fieldset>

        <div className={cx("acceptance-criteria")}>
          {criteria.map((criterion) => {
            const text = criterionText(criterion.statement);
            const result = resultFor(criterion);
            const chosen = evidenceFor(criterion);
            return (
              <fieldset
                className={cx("acceptance-criterion")}
                key={criterion.criterion_id}
              >
                <legend>
                  {text}
                  {criterion.required ? "（必需）" : ""}
                </legend>
                <label className={cx("acceptance-judgement")}>
                  判断
                  <select
                    aria-label={`判断：${text}`}
                    value={result}
                    onChange={(event) =>
                      setResults((current) => ({
                        ...current,
                        [criterion.criterion_id]: event.target.value,
                      }))
                    }
                  >
                    {judgementOptions.map((option) => (
                      <option key={option.value} value={option.value}>
                        {option.label}
                      </option>
                    ))}
                  </select>
                </label>
                <div className={cx("acceptance-evidence")}>
                  <span className={cx("acceptance-evidence-label")}>
                    证据（标记“通过”时至少选择一项）
                  </span>
                  {candidates.map((option) => (
                    <label
                      className={cx("acceptance-evidence-option")}
                      key={`${criterion.criterion_id}-${option.ref}`}
                    >
                      <input
                        type="checkbox"
                        checked={chosen.includes(option.ref)}
                        onChange={(event) =>
                          setEvidence((current) => {
                            const previous =
                              current[criterion.criterion_id] ?? [];
                            return {
                              ...current,
                              [criterion.criterion_id]: event.target.checked
                                ? [...previous, option.ref]
                                : previous.filter((ref) => ref !== option.ref),
                            };
                          })
                        }
                      />
                      <span>{option.label}</span>
                    </label>
                  ))}
                </div>
              </fieldset>
            );
          })}
        </div>

        {missingEvidence && (
          <p className={cx("acceptance-note")} role="status">
            标记为“通过”的验收项至少需要选择一项证据。
          </p>
        )}
        {!missingEvidence && unresolved > 0 && (
          <p className={cx("acceptance-note")}>
            仍有 {unresolved}{" "}
            项必需验收未通过或未核实；提交后任务不会完成，会保留待复验记录。
          </p>
        )}
        {error && (
          <p className={cx("error")} role="alert">
            {error}
          </p>
        )}

        <div className={cx("acceptance-actions")}>
          <button type="button" onClick={requestClose} disabled={busy}>
            取消
          </button>
          <button
            type="submit"
            className={cx("primary")}
            disabled={busy || expired || !candidate || missingEvidence}
          >
            {expired ? "验收依据已变化" : busy ? "正在保存…" : "提交验收"}
          </button>
        </div>
      </form>
    </Modal>
  );
}
