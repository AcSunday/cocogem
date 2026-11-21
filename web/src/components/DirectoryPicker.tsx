import { useEffect, useRef, useState } from "react";
import { api, type DirectoryListing } from "../api/client";
import { t, useLanguage } from "../lib/i18n";
import { errorText } from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import layout from "../styles/layout.module.css";
import conversation from "../styles/conversation.module.css";
import panels from "../styles/panels.module.css";
import settings from "../styles/settings.module.css";
import { Icon } from "./Icon";

const cx = scoped(base, controls, layout, conversation, panels, settings);

export function DirectoryPicker({
  initialPath,
  restricted,
  choose,
  close,
}: {
  initialPath: string;
  restricted: boolean;
  choose: (path: string) => void;
  close: () => void;
}) {
  useLanguage();
  const [path, setPath] = useState(initialPath);
  const [location, setLocation] = useState(initialPath);
  const [listing, setListing] = useState<DirectoryListing>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const locationInput = useRef<HTMLInputElement>(null);
  useEffect(() => {
    let current = true;
    setLoading(true);
    setError("");
    void api<DirectoryListing>(
      `/api/v1/directories?path=${encodeURIComponent(path)}`,
    )
      .then((result) => {
        if (!current) return;
        setListing(result);
        setLocation(result.path);
      })
      .catch((error) => {
        if (current) setError(errorText(error));
      })
      .finally(() => {
        if (current) setLoading(false);
      });
    return () => {
      current = false;
    };
  }, [path]);
  useEffect(() => locationInput.current?.focus(), []);
  return (
    <section
      className={cx("directory-picker")}
      aria-label={t("选择本机目录", "Choose local directory")}
    >
      <strong>{t("选择本机目录", "Choose local directory")}</strong>
      <small className={cx("muted")}>
        {t(
          "选择文件夹后，保存工作空间即可生效。",
          "Select a folder, then save the project to apply.",
        )}
      </small>
      <div className={cx("directory-location")}>
        <label>
          {t("浏览位置", "Path")}
          <input
            ref={locationInput}
            value={location}
            maxLength={4096}
            autoComplete="off"
            spellCheck={false}
            placeholder={t("完整路径或 ~/", "Full path or ~/")}
            onChange={(event) => setLocation(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter" && !event.nativeEvent.isComposing) {
                event.preventDefault();
                setPath(location);
              }
            }}
          />
        </label>
        <button
          type="button"
          onClick={() => setPath(location)}
          disabled={loading}
        >
          {t("前往", "Go")}
        </button>
      </div>
      <div className={cx("directory-navigation")}>
        <button
          type="button"
          disabled={loading || !listing?.parent}
          onClick={() => setPath(listing!.parent)}
        >
          {t("返回上级", "Up one level")}
        </button>
        <button type="button" disabled={loading} onClick={() => setPath("")}>
          {restricted
            ? t("允许的目录", "Allowed roots")
            : t("主目录", "Home directory")}
        </button>
      </div>
      {loading && (
        <p role="status">{t("正在读取目录…", "Loading directory…")}</p>
      )}
      {error && (
        <p className={cx("error")} role="alert">
          {error}
        </p>
      )}
      {listing && !loading && (
        <>
          <p className={cx("directory-current")}>
            <span>{t("当前目录", "Current directory")}</span>
            <span>{listing.path || t("允许的目录", "Allowed roots")}</span>
          </p>
          <ul
            className={cx("directory-list")}
            aria-label={t("子目录", "Subdirectories")}
          >
            {listing.items.map((item) => (
              <li key={item.path}>
                <button type="button" onClick={() => setPath(item.path)}>
                  <Icon name="folder" />
                  <span>{item.name}</span>
                  <Icon name="open" size={14} />
                </button>
              </li>
            ))}
          </ul>
          {!listing.items.length && (
            <p className={cx("muted")}>
              {t("没有可选的子目录", "No subdirectories")}
            </p>
          )}
          {listing.truncated && (
            <p className={cx("muted")}>
              {t(
                "目录条目较多，仅展示部分；可输入路径直接前往。",
                "Showing a subset of folders; enter a path above to jump directly.",
              )}
            </p>
          )}
        </>
      )}
      <div className={cx("form-actions")}>
        <button type="button" onClick={close}>
          {t("取消选择", "Cancel")}
        </button>
        <button
          type="button"
          className={cx("primary")}
          disabled={loading || !!error || !listing?.path}
          onClick={() => choose(listing!.path)}
        >
          {t("选择此目录", "Select this directory")}
        </button>
      </div>
    </section>
  );
}
