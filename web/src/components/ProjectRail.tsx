import { t, useLanguage } from "../lib/i18n";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import layout from "../styles/layout.module.css";
import { BrandLogo } from "./BrandLogo";
import { Icon } from "./Icon";

const cx = scoped(base, layout);

export type ProjectView = "home" | "tasks" | "usage" | "studio";

/** The far-left rail switches the project surface without replacing navigation. */
export function ProjectRail({
  active,
  pendingPermissions = 0,
  onHome,
  onTasks,
  onUsage,
  onStudio,
  onSettings,
}: {
  active: ProjectView;
  pendingPermissions?: number;
  onHome: () => void;
  onTasks: () => void;
  onUsage: () => void;
  onStudio: () => void;
  onSettings: () => void;
}) {
  useLanguage();
  return (
    <aside
      className={cx("project-rail")}
      aria-label={t("项目视图", "Project views")}
    >
      <button
        type="button"
        className={cx("rail-brand")}
        aria-label="Cocogem"
        title="Cocogem"
        onClick={onHome}
      >
        <BrandLogo size={26} />
      </button>
      <button
        type="button"
        className={cx("rail-button")}
        aria-label={t("项目首页", "Project home")}
        title={t("项目首页", "Project home")}
        aria-current={active === "home" ? "page" : undefined}
        onClick={onHome}
      >
        <Icon name="home" size={20} />
      </button>
      <button
        type="button"
        className={cx("rail-button")}
        aria-label={t("任务清单", "Tasks")}
        title={t("任务清单", "Tasks")}
        aria-current={active === "tasks" ? "page" : undefined}
        onClick={onTasks}
      >
        <Icon name="tasks" size={20} />
      </button>
      <button
        type="button"
        className={cx("rail-button")}
        aria-label={t("Tokens 用量", "Token usage")}
        title={t("Tokens 用量", "Token usage")}
        aria-current={active === "usage" ? "page" : undefined}
        onClick={onUsage}
      >
        <Icon name="usage" size={20} />
      </button>
      <button
        type="button"
        className={cx("rail-button")}
        aria-label={t("猫猫研发室", "Cat Studio")}
        title={t("猫猫研发室", "Cat Studio")}
        aria-current={active === "studio" ? "page" : undefined}
        onClick={onStudio}
      >
        <Icon name="studio" size={20} />
        {pendingPermissions > 0 && (
          <span className={cx("rail-badge-dot")} aria-hidden="true" />
        )}
      </button>
      <button
        type="button"
        className={cx("rail-button rail-settings")}
        aria-label={t("设置", "Settings")}
        title={t("设置", "Settings")}
        onClick={onSettings}
      >
        <Icon name="gear" size={20} />
      </button>
    </aside>
  );
}
