import { getCatPreset } from "../lib/cats";
import { initial } from "../lib/presentation";
import { scoped } from "../styles/classes";
import controls from "../styles/controls.module.css";
import conversation from "../styles/conversation.module.css";
import { CatPortrait } from "./CatSprite";

const cx = scoped(controls, conversation);

const imageAvatar = /^data:image\/(?:png|jpeg);base64,[a-z0-9+/=]+$/i;

/** Uploaded avatars are restricted to decoded PNG/JPEG data URIs. */
export const memberImage = (avatar?: string) =>
  avatar && imageAvatar.test(avatar) ? avatar : "";

/** Hash the full name, not the initial, into the eight CSS palette pairs. */
function nameTone(name: string) {
  let hash = 2166136261;
  for (const character of (name ?? "").trim().normalize("NFC")) {
    hash = Math.imul(hash ^ character.codePointAt(0)!, 16777619);
  }
  return (hash >>> 0) % 8;
}

const cliBadgeMeta: Record<string, { letter: string; cls: string; title: string }> = {
  claude: { letter: "C", cls: "cli-claude", title: "Claude Code" },
  codex: { letter: "X", cls: "cli-codex", title: "Codex" },
  gemini: { letter: "G", cls: "cli-gemini", title: "Gemini CLI" },
  pi: { letter: "π", cls: "cli-pi", title: "Pi" },
  opencode: { letter: "O", cls: "cli-opencode", title: "OpenCode" },
};

/**
 * A member's built-in pixel cat portrait, uploaded image, or name-colored
 * initial; decorative, with the name shown beside it. Optionally displays a
 * compact CLI micro-badge on the bottom-right corner.
 */
export function Avatar({
  name,
  avatar,
  executor,
  small,
}: {
  name: string;
  avatar?: string;
  executor?: string;
  small?: boolean;
}) {
  const cat = getCatPreset(avatar);
  const image = cat ? "" : memberImage(avatar);
  const badge = executor ? cliBadgeMeta[executor.toLowerCase()] : undefined;

  return (
    <span
      className={cx(
        `avatar ${small ? "avatar-sm" : ""} ${cat ? "avatar-cat" : image ? "avatar-image" : `avatar-tone-${nameTone(name)}`}`,
      )}
      aria-hidden="true"
    >
      {cat ? (
        <CatPortrait preset={cat} size={small ? 22 : 30} />
      ) : image ? (
        <img src={image} alt="" />
      ) : (
        initial(name ?? "")
      )}
      {badge && (
        <span
          className={cx(`avatar-cli-badge ${badge.cls}`)}
          data-cli={badge.letter}
          title={badge.title}
        />
      )}
    </span>
  );
}
