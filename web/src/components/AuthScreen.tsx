import { useEffect, useState } from "react";
import { api, APIError } from "../api/client";
import { errorText } from "../lib/presentation";
import { cx } from "../styles/classes";
import { Icon } from "./Icon";

export function AuthScreen({
  required,
  error,
  connect,
}: {
  required: boolean;
  error: string;
  connect: () => Promise<void>;
}) {
  const [initialized, setInitialized] = useState<boolean>();
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!required) return;
    let canceled = false;
    void api<{ initialized: boolean }>("/auth/status")
      .then((status) => {
        if (!canceled) setInitialized(status.initialized);
      })
      .catch((error) => {
        if (!canceled) setMessage(errorText(error));
      });
    return () => {
      canceled = true;
    };
  }, [required]);
  const setup = initialized === false;
  return (
    <main className={cx("bootstrap")}>
      <section className={cx("auth-card")} aria-labelledby="auth-title">
        <span className={cx("brand-mark")}>
          <Icon name="logo" size={22} />
        </span>
        <p className={cx("auth-brand")}>Cocogem</p>
        <h1 id="auth-title">
          {setup
            ? "设置你的工作空间密码"
            : required
              ? "欢迎回来"
              : "正在连接工作空间"}
        </h1>
        <p className={cx("muted")}>
          {setup
            ? "首次使用，设置一个密码来保护本机工作空间。"
            : required
              ? "输入密码，继续你的协作。"
              : "正在连接本地服务…"}
        </p>
        {initialized !== undefined && (
          <form
            onSubmit={async (event) => {
              event.preventDefault();
              if (busy) return;
              if (setup && password !== confirmation) {
                setMessage("两次输入的密码不一致");
                return;
              }
              setBusy(true);
              setMessage("");
              try {
                await api(setup ? "/auth/setup" : "/auth/login", "POST", {
                  password,
                });
                setPassword("");
                setConfirmation("");
                await connect();
              } catch (error) {
                if (error instanceof APIError && error.status === 409) {
                  const status = await api<{ initialized: boolean }>(
                    "/auth/status",
                  ).catch(() => null);
                  if (status) setInitialized(status.initialized);
                }
                setMessage(errorText(error));
              } finally {
                setBusy(false);
              }
            }}
          >
            <label>
              {setup ? "设置密码" : "密码"}
              <input
                type="password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                autoComplete={setup ? "new-password" : "current-password"}
                minLength={12}
                maxLength={128}
                required
                autoFocus
                aria-describedby={setup ? "password-hint" : undefined}
                disabled={busy}
              />
            </label>
            {setup && (
              <>
                <p id="password-hint" className={cx("muted")}>
                  12–128 个字符，可以使用好记的短语。
                </p>
                <label>
                  确认密码
                  <input
                    type="password"
                    value={confirmation}
                    onChange={(event) => setConfirmation(event.target.value)}
                    autoComplete="new-password"
                    minLength={12}
                    maxLength={128}
                    required
                    disabled={busy}
                  />
                </label>
              </>
            )}
            {(message || error) && (
              <p role="alert" className={cx("error")}>
                {message || error}
              </p>
            )}
            <button className={cx("primary")} disabled={busy}>
              {busy ? "正在解锁…" : setup ? "设置密码并进入" : "解锁工作空间"}
            </button>
          </form>
        )}
        {initialized === undefined && (message || error) && (
          <>
            <p role="alert" className={cx("error")}>
              {message || error}
            </p>
            <button type="button" onClick={() => location.reload()}>
              重新连接
            </button>
          </>
        )}
        <p className={cx("auth-footer")}>本地工作空间 · 数据留在本机</p>
      </section>
    </main>
  );
}
