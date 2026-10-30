import { Select } from "./Select";
import { cx } from "../styles/classes";
import { Icon } from "./Icon";

export type Appearance = "system" | "light" | "dark" | "warm" | "deepseek";

export function AppearanceSelect({
  value,
  onChange,
}: {
  value: Appearance;
  onChange: (value: Appearance) => void;
}) {
  return (
    <label className={cx("appearance-select")}>
      <Icon name="appearance" size={15} />
      <span>外观</span>
      <Select
        aria-label="外观模式"
        value={value}
        onChange={(selected) => onChange(selected as Appearance)}
      >
        <option value="system">跟随系统</option>
        <option value="light">浅色</option>
        <option value="dark">深色</option>
        <option value="warm">经典暖色</option>
        <option value="deepseek">DeepSeek</option>
      </Select>
    </label>
  );
}
