import { initial, memberTone } from "../lib/presentation";
import { cx } from "../styles/classes";

/** A member's initial on a stable tone; decorative, the name is always shown beside it. */
export function Avatar({
  name,
  id,
  index = 0,
  small,
}: {
  name: string;
  id?: string;
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
      {initial(name)}
    </span>
  );
}
