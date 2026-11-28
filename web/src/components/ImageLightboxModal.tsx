import { useEffect, useRef, useState } from "react";
import { t } from "../lib/i18n";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import panels from "../styles/panels.module.css";
import { Modal } from "./Modal";

const cx = scoped(base, controls, panels);

export type ImageLightboxModalProps = {
  src: string;
  title: string;
  downloadUrl?: string;
  close: () => void;
};

/** Full-resolution image lightbox with fit/actual-size toggle and original download. */
export function ImageLightboxModal({
  src,
  title,
  downloadUrl,
  close,
}: ImageLightboxModalProps) {
  const [actualSize, setActualSize] = useState(false);
  const [loadError, setLoadError] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    setActualSize(false);
    setLoadError(false);
    containerRef.current?.scrollTo({ top: 0, left: 0 });
  }, [src]);

  return (
    <Modal title={title || t("视觉证据预览", "Visual Evidence Preview")} close={close} wide>
      <div className={cx("lightbox-shell")} data-testid="image-lightbox-modal">
        <div className={cx("lightbox-toolbar")}>
          <button
            type="button"
            className={cx("ghost", "compact")}
            aria-pressed={actualSize}
            onClick={() => setActualSize((prev) => !prev)}
          >
            {actualSize
              ? t("适应窗口", "Fit to view")
              : t("原始尺寸 (1:1)", "Actual size (1:1)")}
          </button>
          {(downloadUrl || src) && (
            <a
              className={cx("ghost", "compact")}
              href={downloadUrl || src}
              target="_blank"
              rel="noreferrer"
              download={title || undefined}
            >
              {t("下载原图", "Download")}
            </a>
          )}
        </div>
        <div
          ref={containerRef}
          className={cx(
            "lightbox-stage",
            actualSize && "lightbox-stage-actual",
          )}
        >
          {loadError ? (
            <p className={cx("error")} role="alert">
              {t(
                "图片加载失败，可能已被移动或无法访问。",
                "Failed to load image; it may have been moved or is unavailable.",
              )}
            </p>
          ) : (
            <img
              src={src}
              alt={title}
              className={cx(
                "lightbox-image",
                actualSize && "lightbox-image-actual",
              )}
              onError={() => setLoadError(true)}
            />
          )}
        </div>
      </div>
    </Modal>
  );
}
