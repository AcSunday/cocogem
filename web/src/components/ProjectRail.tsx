import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import layout from "../styles/layout.module.css";
import { Icon } from "./Icon";

const cx = scoped(base, layout);

export type ProjectView = "home" | "tasks";

/** The far-left rail switches the project surface without replacing navigation. */
export function ProjectRail({
  active,
  onHome,
  onTasks,
  onSettings,
}: {
  active: ProjectView;
  onHome: () => void;
  onTasks: () => void;
  onSettings: () => void;
}) {
  return (
    <aside className={cx("project-rail")} aria-label="项目视图">
      <button
        type="button"
        className={cx("rail-button")}
        aria-label="项目首页"
        title="项目首页"
        aria-current={active === "home" ? "page" : undefined}
        onClick={onHome}
      >
        <Icon name="home" size={20} />
      </button>
      <button
        type="button"
        className={cx("rail-button")}
        aria-label="任务清单"
        title="任务清单"
        aria-current={active === "tasks" ? "page" : undefined}
        onClick={onTasks}
      >
        <Icon name="tasks" size={20} />
      </button>
      <button
        type="button"
        className={cx("rail-button rail-settings")}
        aria-label="设置"
        title="设置"
        onClick={onSettings}
      >
        <Icon name="gear" size={20} />
      </button>
    </aside>
  );
}
