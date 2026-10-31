import { initial, memberTone } from "../lib/presentation";
import { scoped } from "../styles/classes";
import controls from "../styles/controls.module.css";
import conversation from "../styles/conversation.module.css";

const cx = scoped(controls, conversation);

const imageAvatar = /^data:image\/(?:png|jpeg);base64,[a-z0-9+/=]+$/i;

/** Uploaded avatars are restricted to decoded PNG/JPEG data URIs. */
export const memberImage = (avatar?: string) =>
  avatar && imageAvatar.test(avatar) ? avatar : "";

/** A member's uploaded image or initial on a stable tone; decorative, the name is always shown beside it. */
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
  const image = memberImage(avatar);
  return (
    <span
      className={cx(
        `avatar tone-${id ? memberTone(id) : Math.max(0, index) % 4} ${small ? "avatar-sm" : ""} ${image ? "avatar-image" : ""}`,
      )}
      aria-hidden="true"
    >
      {image ? <img src={image} alt="" /> : initial(name)}
    </span>
  );
}
