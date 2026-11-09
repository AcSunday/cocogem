import type { Workspace } from "../api/client";
import type { WorkflowKey } from "./Composer";
import { Icon } from "./Icon";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import conversation from "../styles/conversation.module.css";

const cx = scoped(base, conversation);

const scenarios = [
  {
    key: "discussion",
    icon: "discussion",
    title: "一起讨论",
    description: "让不同观点独立表达，再梳理共识与分歧。",
    sample: "比较自己实现 Agent Loop 和使用 Agent 框架的取舍。",
  },
  {
    key: "research",
    icon: "research",
    title: "做一次研究协作",
    description: "查找资料、核对来源，形成可追溯的报告。",
    sample: "研究多 Agent 协作的适用场景，说明证据和限制。",
  },
  {
    key: "engineering",
    icon: "code",
    title: "工程协作",
    description: "使用成员已获授权的能力执行，独立复核后交付。",
    sample: "分析示例仓库如何区分成员身份与任务职责。",
  },
] as const;

/** Without a workspace there is one thing to do; with one, scenarios prefill the composer. */
export function Welcome({
  workspace,
  members,
  onCreate,
  onPick,
}: {
  workspace?: Workspace;
  members: number;
  onCreate: () => void;
  onPick: (key: WorkflowKey, sample: string) => void;
}) {
  if (!workspace)
    return (
      <div className={cx("welcome")}>
        <h1>把问题交给一个团队。</h1>
        <div className={cx("onboarding")}>
          <p>直接在下方描述目标。首次发送会创建“未命名项目”和可用成员，无需先填写项目表单。</p>
          <ol>
            <li>选择讨论、研究协作或工程协作，提出第一个问题</li>
            <li>在任务页跟踪目标、补充、来源和验证</li>
            <li>选择确切成果与证据，单独记录验收</li>
          </ol>
          <button className={cx("ghost")} onClick={onCreate}>
            <Icon name="plus" /> 高级：手动创建项目
          </button>
        </div>
      </div>
    );
  return (
    <div className={cx("welcome")}>
      <h1>把问题交给一个团队。</h1>
      <p className={cx("intro")}>
        直接提出问题，也可以附上链接。成员会主动查证、交流与复核，把过程和结论留在这里。
      </p>
      <div className={cx("scenario-grid")}>
        {scenarios.map((item) => (
          <button
            key={item.key}
            className={cx("scenario")}
            onClick={() => onPick(item.key, item.sample)}
          >
            <span className={cx("scenario-icon")}>
              <Icon name={item.icon} size={18} />
            </span>
            <strong>{item.title}</strong>
            <p>{item.description}</p>
          </button>
        ))}
      </div>
      <p className={cx("welcome-note")}>
        {members} 位成员已就位。可以描述你需要的分工，或输入 @ 指定成员。
      </p>
    </div>
  );
}
