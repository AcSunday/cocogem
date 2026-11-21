import type { Artifact } from "../api/client";
import { t, useLanguage } from "../lib/i18n";
import { artifactTypeLabel } from "../lib/presentation";
import { scoped } from "../styles/classes";
import panels from "../styles/panels.module.css";
import { Icon } from "./Icon";

const cx = scoped(panels);

export function ArtifactCard({
  artifact,
  onOpen,
}: {
  artifact: Artifact;
  onOpen: (artifact: Artifact) => void;
}) {
  useLanguage();
  return (
    <button className={cx("artifact-card")} onClick={() => onOpen(artifact)}>
      <Icon name="report" size={20} />
      <span className={cx("artifact-card-content")}>
        <strong>{artifact.title}</strong>
        {!!artifact.files?.length && (
          <small>{artifact.files.map((file) => file.name).join(" · ")}</small>
        )}
        <span className={cx("card-kicker")}>
          {artifactTypeLabel(artifact.type)} · v{artifact.version}
        </span>
        <small>
          {artifact.feedback === "accepted"
            ? t("已接受", "Accepted")
            : artifact.feedback === "dismissed"
              ? t("已搁置", "Dismissed")
              : t("待查看", "Unread")}
        </small>
      </span>
      <Icon name="open" size={14} />
    </button>
  );
}
