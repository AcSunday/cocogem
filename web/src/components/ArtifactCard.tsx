import type { Artifact } from "../api/client";
import { artifactTypeLabel } from "../lib/presentation";
import { cx } from "../styles/classes";
import { Icon } from "./Icon";

export function ArtifactCard({
  artifact,
  onOpen,
}: {
  artifact: Artifact;
  onOpen: (artifact: Artifact) => void;
}) {
  return (
    <button className={cx("artifact-card")} onClick={() => onOpen(artifact)}>
      <Icon name="report" size={20} />
      <span className={cx("artifact-card-content")}>
        <strong>{artifact.title}</strong>
        <span className={cx("card-kicker")}>
          {artifactTypeLabel(artifact.type)} · v{artifact.version}
        </span>
        <small>
          {artifact.feedback === "accepted"
            ? "已接受"
            : artifact.feedback === "dismissed"
              ? "已搁置"
              : "待查看"}
        </small>
      </span>
      <Icon name="open" size={14} />
    </button>
  );
}
