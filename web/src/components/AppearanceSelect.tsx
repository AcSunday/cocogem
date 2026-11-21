import { Select } from "./Select";
import { t, useLanguage } from "../lib/i18n";
import { scoped } from "../styles/classes";
import layout from "../styles/layout.module.css";
import { Icon } from "./Icon";

const cx = scoped(layout);

export type Appearance = "system" | "light" | "dark" | "warm";

export function AppearanceSelect({
  value,
  onChange,
}: {
  value: Appearance;
  onChange: (value: Appearance) => void;
}) {
  useLanguage();
  return (
    <label className={cx("appearance-select")}>
      <Icon name="appearance" size={15} />
      <span>{t("外观", "Theme")}</span>
      <Select
        aria-label={t("外观模式", "Appearance mode")}
        value={value}
        onChange={(selected) => onChange(selected as Appearance)}
      >
        <option value="system">{t("跟随系统", "System")}</option>
        <option value="light">{t("浅色", "Light")}</option>
        <option value="dark">{t("深色", "Dark")}</option>
        <option value="warm">{t("经典暖色", "Warm")}</option>
      </Select>
    </label>
  );
}
