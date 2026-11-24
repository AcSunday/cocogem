import { useEffect, useState } from "react";
import { api, APIError } from "../api/client";
import { type Language, t, useLanguage } from "../lib/i18n";
import { errorText } from "../lib/presentation";
import { scoped } from "../styles/classes";
import base from "../styles/base.module.css";
import controls from "../styles/controls.module.css";
import layout from "../styles/layout.module.css";
import conversation from "../styles/conversation.module.css";
import panels from "../styles/panels.module.css";
import { BrandLogo } from "./BrandLogo";
import { Icon } from "./Icon";
import { Select } from "./Select";

const cx = scoped(base, controls, layout, conversation, panels);

export function AuthScreen({
  required,
  error,
  connect,
}: {
  required: boolean;
  error: string;
  connect: () => Promise<void>;
}) {
  const [language, setLanguage] = useLanguage();
  const [initialized, setInitialized] = useState<boolean>();
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [visible, setVisible] = useState(false);
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
        <div className={cx("auth-head")}>
          <span className={cx("brand-mark")}>
            <BrandLogo size={28} />
          </span>
          <p className={cx("auth-brand")}>Cocogem</p>
        </div>
        <div className={cx("auth-intro")}>
          <h1 id="auth-title">
            {setup
              ? t("设置你的工作空间密码", "Set Your Workspace Password")
              : required
                ? t("欢迎回来", "Welcome Back")
                : t("正在连接工作空间", "Connecting to workspace")}
          </h1>
          <p className={cx("muted")}>
            {setup
              ? t(
                  "首次使用，请选择界面语言并设置密码来保护本机工作空间。",
                  "First time here — choose your language and set a password to protect your local workspace.",
                )
              : required
                ? t("输入密码，继续你的协作。", "Enter your password to continue.")
                : t("正在连接本地服务…", "Connecting to local service…")}
          </p>
          {initialized === undefined && !(message || error) && (
            <span
              className={cx("auth-spinner")}
              role="status"
              aria-label={t("正在连接工作空间", "Connecting to workspace")}
            />
          )}
        </div>
        {initialized !== undefined && (
          <form
            onSubmit={async (event) => {
              event.preventDefault();
              if (busy) return;
              if (setup && password !== confirmation) {
                setMessage(
                  t("两次输入的密码不一致", "Passwords do not match"),
                );
                return;
              }
              if (setup) {
                setLanguage(language);
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
            {setup && (
              <label>
                {t("界面语言", "Language")}
                <Select
                  aria-label={t("界面语言", "Language")}
                  value={language}
                  onChange={(selected) => setLanguage(selected as Language)}
                  disabled={busy}
                >
                  <option value="zh-CN">简体中文</option>
                  <option value="en">English</option>
                </Select>
              </label>
            )}
            <label>
              {setup ? t("设置密码", "Set password") : t("密码", "Password")}
              <span className={cx("password-field")}>
                <input
                  type={visible ? "text" : "password"}
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
                <button
                  type="button"
                  className={cx("password-toggle")}
                  aria-label={
                    visible
                      ? t("隐藏密码", "Hide password")
                      : t("显示密码", "Show password")
                  }
                  aria-pressed={visible}
                  onClick={() => setVisible((shown) => !shown)}
                >
                  <Icon name={visible ? "eyeOff" : "eye"} size={16} />
                </button>
              </span>
            </label>
            {setup && (
              <>
                <p id="password-hint" className={cx("muted")}>
                  {t(
                    "12–128 个字符，可以使用好记的短语。",
                    "12–128 characters. A memorable passphrase works well.",
                  )}
                </p>
                <label>
                  {t("确认密码", "Confirm password")}
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
              {busy
                ? t("正在解锁…", "Unlocking…")
                : setup
                  ? t("设置密码并进入", "Set Password & Enter")
                  : t("解锁工作空间", "Unlock Workspace")}
            </button>
          </form>
        )}
        {initialized === undefined && (message || error) && (
          <>
            <p role="alert" className={cx("error")}>
              {message || error}
            </p>
            <button type="button" onClick={() => location.reload()}>
              {t("重新连接", "Reconnect")}
            </button>
          </>
        )}
        <p className={cx("auth-footer")}>
          {t(
            "本地工作空间 · 数据留在本机",
            "Local workspace · Data stays on your machine",
          )}
        </p>
      </section>
    </main>
  );
}
