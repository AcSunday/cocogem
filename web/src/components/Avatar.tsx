import { initial } from "../lib/presentation";
import { cx } from "../styles/classes";

/** A member's initial on a stable tone; decorative, the name is always shown beside it. */
export function Avatar({
  name,
  index,
  small,
}: {
  name: string;
  index: number;
  small?: boolean;
}) {
  return (
    <span
      className={cx(
        `avatar tone-${Math.max(0, index) % 4} ${small ? "avatar-sm" : ""}`,
      )}
      aria-hidden="true"
    >
      {initial(name)}
    </span>
  );
}
