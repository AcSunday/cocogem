import { useEffect, useRef, useState } from "react";
import { api, type DirectoryListing } from "../api/client";
import { errorText } from "../lib/presentation";
import { cx } from "../styles/classes";
import { Icon } from "./Icon";

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
    <section className={cx("directory-picker")} aria-label="选择本机目录">
      <strong>选择本机目录</strong>
      <small className={cx("muted")}>
        选择文件夹后，保存工作空间即可生效。
      </small>
      <div className={cx("directory-location")}>
        <label>
          浏览位置
          <input
            ref={locationInput}
            value={location}
            maxLength={4096}
            autoComplete="off"
            spellCheck={false}
            placeholder="完整路径或 ~/"
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
          前往
        </button>
      </div>
      <div className={cx("directory-navigation")}>
        <button
          type="button"
          disabled={loading || !listing?.parent}
          onClick={() => setPath(listing!.parent)}
        >
          返回上级
        </button>
        <button type="button" disabled={loading} onClick={() => setPath("")}>
          {restricted ? "允许的目录" : "主目录"}
        </button>
      </div>
      {loading && <p role="status">正在读取目录…</p>}
      {error && (
        <p className={cx("error")} role="alert">
          {error}
        </p>
      )}
      {listing && !loading && (
        <>
          <p className={cx("directory-current")}>
            <span>当前目录</span>
            <span>{listing.path || "允许的目录"}</span>
          </p>
          <ul className={cx("directory-list")} aria-label="子目录">
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
            <p className={cx("muted")}>没有可选的子目录</p>
          )}
          {listing.truncated && (
            <p className={cx("muted")}>
              目录条目较多，仅展示部分；可输入路径直接前往。
            </p>
          )}
        </>
      )}
      <div className={cx("form-actions")}>
        <button type="button" onClick={close}>
          取消选择
        </button>
        <button
          type="button"
          className={cx("primary")}
          disabled={loading || !!error || !listing?.path}
          onClick={() => choose(listing!.path)}
        >
          选择此目录
        </button>
      </div>
    </section>
  );
}
