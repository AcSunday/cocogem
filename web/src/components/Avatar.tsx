import { initial, memberTone } from "../lib/presentation";
import { cx } from "../styles/classes";

/** The built-in choices accepted by the server; no URLs, SVG or uploads. */
export const avatars = ["🐱", "🦊", "🦉", "🐼", "🐰", "🐻", "🐧", "🐯"];

/** A member's chosen avatar or initial on a stable tone; decorative, the name is always shown beside it. */
export function Avatar({
  name,
  id,
  avatar,
  index = 0,
  small,
}: {
  name: string;
  id?: string;
  avatar?: string;
  index?: number;
  small?: boolean;
}) {
  return (
    <span
      className={cx(
        `avatar tone-${id ? memberTone(id) : Math.max(0, index) % 4} ${small ? "avatar-sm" : ""}`,
      )}
      aria-hidden="true"
    >
      {avatar && avatars.includes(avatar) ? avatar : initial(name)}
    </span>
  );
}
