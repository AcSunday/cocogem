import { useState } from "react";
import type { Run, System } from "../api/client";
import { api, statusText } from "../api/client";
import { cx } from "../styles/classes";
import {
  errorText,
  formatDateTime,
  limitLabel,
  workflows,
} from "../lib/presentation";
import { Modal } from "./Modal";

export function SettingsDialog({
  system,
  setSystem,
  close,
}: {
  system: System;
  setSystem: (value: System) => void;
  close: () => void;
}) {
  const [executor, setExecutor] = useState(
    system.executors?.find((item) => item.installed && item.supported !== false)
      ?.id ?? "claude",
  );
  const [model, setModel] = useState("");
  const [verifying, setVerifying] = useState(false);
  const [error, setError] = useState("");
  const key = `${executor}:${model.trim() || "default"}`;
  const runnable = system.executors?.some(
    (item) =>
      item.id === executor && item.installed && item.supported !== false,
  );
  return (
    <Modal title="服务与设置" close={close}>
      <p>
        {system.demo
          ? "当前是离线演示：成员使用确定性示例模型，只读取随包资料，不会联网或产生用量。"
          : "成员调用本机 Claude、Codex 或 Gemini CLI，复用本机的模型、供应商与认证配置。网页不会读取凭证。"}
      </p>
      <dl>
        <dt>执行限制</dt>
        <dd>
          <ul>
            {Object.entries(system.limits ?? {}).map(([name, value]) => (
              <li key={name}>{limitLabel(name, value)}</li>
            ))}
          </ul>
        </dd>
        <dt>{system.demo ? "模型" : "本机 CLI"}</dt>
        <dd>
          {system.demo
            ? "离线演示模型"
            : system.executors?.map((item) => (
                <p key={item.id}>
                  {item.id} ·{" "}
                  {item.installed ? `已安装 · ${item.version}` : "未检测到"}
                  {item.reason
                    ? ` · ${item.supported === false ? "不支持" : "使用限制"}：${item.reason}`
                    : ""}
                </p>
              ))}
        </dd>
        <dt>在线搜索</dt>
        <dd>
          {system.demo ? "离线演示中不可用" : statusText[system.search_status]}
        </dd>
        <dt>允许的资料目录</dt>
        <dd>{system.roots.join("\n") || "未设置"}</dd>
      </dl>
      {!system.demo && (
        <>
          <p className={cx("muted")}>
            真实运行会通过 CLI
            发送问题与读取片段至模型服务；搜索会发送查询词。已安装不代表连接或模型可用。
          </p>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              setVerifying(true);
              setError("");
              void api("/api/v1/system/verify", "POST", {
                executor,
                model: model.trim() || "default",
              })
                .then(() => api<System>("/api/v1/system"))
                .then(setSystem)
                .catch((error) => setError(errorText(error)))
                .finally(() => setVerifying(false));
            }}
          >
            <label>
              验证 CLI
              <select
                value={executor}
                onChange={(event) => {
                  setExecutor(event.target.value);
                  setError("");
                }}
              >
                {["claude", "codex", "gemini"].map((id) => (
                  <option key={id} value={id}>
                    {id}
                  </option>
                ))}
              </select>
            </label>
            <label>
              验证模型（留空使用 CLI 默认）
              <input
                value={model}
                onChange={(event) => {
                  setModel(event.target.value);
                  setError("");
                }}
                placeholder="CLI 默认模型"
              />
            </label>
            {error && (
              <p role="alert" className={cx("error")}>
                {error}
              </p>
            )}
            {system.verified[key] && (
              <p role="status">
                验证时间：{formatDateTime(system.verified[key])} · 该 CLI
                与模型上次已验证
              </p>
            )}
            <div className={cx("form-actions")}>
              <button
                className={cx("primary")}
                disabled={verifying || !runnable}
              >
                {verifying ? "正在验证…" : "验证模型连接（可能产生用量）"}
              </button>
            </div>
          </form>
        </>
      )}
    </Modal>
  );
}

export function SnapshotDialog({
  run,
  close,
}: {
  run: Run;
  close: () => void;
}) {
  const workflow =
    workflows[(run.input.workflow || "free") as keyof typeof workflows];
  return (
    <Modal title="本次协作配置快照" close={close}>
      <p>
        {workflow} · 创建于 {formatDateTime(run.created_at)}
      </p>
      {run.agents?.map((agent) => (
        <section className={cx("snapshot-member")} key={agent.id}>
          <h3>
            {agent.display_name} (@{agent.handle})
          </h3>
          <p className={cx("muted")}>
            配置版本 {agent.revision} ·{" "}
            {agent.executor ? `${agent.executor} · ` : ""}
            {agent.model === "default"
              ? "CLI 默认模型"
              : agent.model === "demo"
                ? "离线演示模型"
                : agent.model}
          </p>
          {agent.role_description && <p>{agent.role_description}</p>}
          <p className={cx("muted")}>可用工具：{agent.tools?.length ?? 0} 项</p>
        </section>
      ))}
      <p className={cx("muted")}>
        历史快照不随成员改名、模型或权限编辑而改变。
      </p>
    </Modal>
  );
}
