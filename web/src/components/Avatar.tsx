import { initial } from "../lib/presentation";
import { scoped } from "../styles/classes";
import controls from "../styles/controls.module.css";
import conversation from "../styles/conversation.module.css";

const cx = scoped(controls, conversation);

const imageAvatar = /^data:image\/(?:png|jpeg);base64,[a-z0-9+/=]+$/i;

/** Uploaded avatars are restricted to decoded PNG/JPEG data URIs. */
export const memberImage = (avatar?: string) =>
  avatar && imageAvatar.test(avatar) ? avatar : "";

/** Hash the full name, not the initial, into the eight CSS palette pairs. */
function nameTone(name: string) {
  let hash = 2166136261;
  for (const character of name.trim().normalize("NFC")) {
    hash = Math.imul(hash ^ character.codePointAt(0)!, 16777619);
  }
  return (hash >>> 0) % 8;
}

/** A member's uploaded image or name-colored initial; decorative, with the name shown beside it. */
export function Avatar({
  name,
  avatar,
  small,
}: {
  name: string;
  avatar?: string;
  small?: boolean;
}) {
  const image = memberImage(avatar);
  return (
    <span
      className={cx(
        `avatar ${small ? "avatar-sm" : ""} ${image ? "avatar-image" : `avatar-tone-${nameTone(name)}`}`,
      )}
      aria-hidden="true"
    >
      {image ? <img src={image} alt="" /> : initial(name)}
    </span>
  );
}
