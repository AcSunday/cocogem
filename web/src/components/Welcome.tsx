import type { Workspace } from "../api/client";
import { isEnglish, t, useLanguage } from "../lib/i18n";
import { Icon } from "./Icon";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import conversation from "../styles/conversation.module.css";

const cx = scoped(base, conversation);

/**
 * Example prompts prefill the Composer only. They carry no preset workflow: the
 * single dynamic kernel decides the collaboration method from the real request.
 */
const samplesZh = [
  "比较自己实现 Agent Loop 和使用 Agent 框架的取舍。",
  "研究多 Agent 协作的适用场景，说明证据和限制。",
  "分析示例仓库如何区分成员身份与任务职责。",
];

const samplesEn = [
  "Compare building a custom Agent Loop versus adopting an existing Agent framework.",
  "Research practical use cases for multi-agent collaboration, noting evidence and trade-offs.",
  "Analyze how the repository separates member identities from task responsibilities.",
];

/** Without a workspace there is one thing to do; with one, a prompt prefills the composer. */
export function Welcome({
  workspace,
  members,
  onCreate,
  onPick,
}: {
  workspace?: Workspace;
  members: number;
  onCreate: () => void;
  onPick: (sample: string) => void;
}) {
  useLanguage();
  const samples = isEnglish() ? samplesEn : samplesZh;
  if (!workspace)
    return (
      <div className={cx("welcome")}>
        <h1>{t("把问题交给一个团队。", "Hand your problem to a team.")}</h1>
        <div className={cx("onboarding")}>
          <p>
            {t(
              "直接在下方描述目标。首次发送会创建“未命名项目”和可用成员，无需先填写项目表单。",
              "Describe your goal below. Sending your first message automatically sets up an “Untitled Project” and available team members — no project form required.",
            )}
          </p>
          <ol>
            <li>
              {t(
                "直接描述目标，或用 @ 指定成员",
                "Describe your goal directly, or @mention a specific member",
              )}
            </li>
            <li>
              {t(
                "在任务页跟踪目标、补充、来源和验证",
                "Track goals, follow-ups, sources, and verification on the Tasks view",
              )}
            </li>
            <li>
              {t(
                "选择确切成果与证据，单独记录验收",
                "Review deliverables and evidence, then record acceptance",
              )}
            </li>
          </ol>
          <button className={cx("ghost")} onClick={onCreate}>
            <Icon name="plus" />{" "}
            {t("高级：手动创建项目", "Advanced: Create project manually")}
          </button>
        </div>
      </div>
    );
  return (
    <div className={cx("welcome")}>
      <h1>{t("把问题交给一个团队。", "Hand your problem to a team.")}</h1>
      <p className={cx("intro")}>
        {t(
          "直接提出问题，也可以附上链接。成员会主动查证、交流与复核，把过程和结论留在这里。",
          "Ask a question or share links. Team members will research, cross-check, and review together, keeping their process and conclusions right here.",
        )}
      </p>
      <div className={cx("scenario-grid")}>
        {samples.map((sample) => (
          <button
            key={sample}
            className={cx("scenario")}
            onClick={() => onPick(sample)}
          >
            <span className={cx("scenario-icon")}>
              <Icon name="layers" size={18} />
            </span>
            <p>{sample}</p>
          </button>
        ))}
      </div>
      <p className={cx("welcome-note")}>
        {t(
          `${members} 位成员已就位。可以描述你需要的分工，或输入 @ 指定成员。`,
          `${members} member${members === 1 ? "" : "s"} ready. Describe what you need or type @ to assign a specific member.`,
        )}
      </p>
    </div>
  );
}
