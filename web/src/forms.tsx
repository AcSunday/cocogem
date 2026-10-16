import { useState } from "react";
import type { Workspace, Agent, Message, Thread, System } from "./api";
import { statusText } from "./api";
import { cx } from "./styles";
import { Modal } from "./Modal";
import { Markdown } from "./Markdown";
const workflows = {
  free: "自由协作",
  discussion: "协作讨论",
  research: "Research",
  code_analysis: "代码分析",
};
const allTools = [
  "list_files",
  "read_file",
  "search_content",
  "web_search",
  "fetch_url",
  "read_source",
  "publish_artifact",
];
const errorText = (error: unknown) =>
  error instanceof Error ? error.message : "操作失败";
const catIcon = (index: number) => ["◈", "✦", "●", "◐"][index % 4];

export function MessageCard({
  message,
  agents,
}: {
  message: Message;
  agents: Agent[];
}) {
  const index = Math.max(
    0,
    agents.findIndex((a) => a.id === message.agent_id),
  );
  return (
    <article
      className={cx(`message ${message.role === "user" ? "user-message" : ""}`)}
    >
      <div
        className={cx(
          `avatar ${message.role === "user" ? "user-avatar" : `cat-${index % 3}`}`,
        )}
      >
        {message.role === "user" ? "你" : catIcon(index)}
      </div>
      <div className={cx("message-content")}>
        <div className={cx("message-meta")}>
          <strong>{message.agent_name}</strong>
          <time>
            {new Date(message.created_at).toLocaleTimeString([], {
              hour: "2-digit",
              minute: "2-digit",
            })}
          </time>
          {message.status !== "completed" && (
            <small>{statusText[message.status]}</small>
          )}
        </div>
        <Markdown text={message.content || "正在准备回复…"} />
      </div>
    </article>
  );
}

export function WorkspaceForm({
  current,
  system,
  close,
  save,
}: {
  current?: Workspace;
  system: System;
  close: () => void;
  save: (value: Workspace) => Promise<void>;
}) {
  const [value, setValue] = useState<Workspace>(
    current ?? {
      id: "",
      name: "",
      description: "",
      root: "",
      consent_version: 0,
      revision: 1,
      created_at: "",
    },
  );
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  return (
    <Modal title={current ? "编辑工作空间" : "创建工作空间"} close={close}>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          setSaving(true);
          void save(value)
            .catch((error) => setError(errorText(error)))
            .finally(() => setSaving(false));
        }}
      >
        <label>
          名称
          <input
            autoFocus
            required
            maxLength={100}
            value={value.name}
            onChange={(event) =>
              setValue({ ...value, name: event.target.value })
            }
            placeholder="例如：我的研究室"
          />
        </label>
        <label>
          说明
          <textarea
            value={value.description}
            onChange={(event) =>
              setValue({ ...value, description: event.target.value })
            }
            placeholder="这个空间想一起完成什么？"
          />
        </label>
        <label>
          本地资料目录
          <select
            value={value.root}
            onChange={(event) =>
              setValue({ ...value, root: event.target.value })
            }
          >
            <option value="">暂不绑定</option>
            {system.roots.map((root) => (
              <option key={root}>{root}</option>
            ))}
          </select>
        </label>
        <label className={cx("check-label")}>
          <input
            type="checkbox"
            checked={value.consent_version >= 1}
            onChange={(event) =>
              setValue({
                ...value,
                consent_version: event.target.checked ? 1 : 0,
              })
            }
          />
          我理解真实模式会将问题和资料片段发送至配置的模型/搜索服务。
        </label>
        {error && (
          <p className={cx("error")} role="alert">
            {error}
          </p>
        )}
        <button className={cx("primary")} disabled={saving}>
          {saving ? "保存中…" : "保存工作空间"}
        </button>
      </form>
    </Modal>
  );
}

export function MembersForm({
  agents,
  system,
  workspaceID,
  close,
  save,
}: {
  agents: Agent[];
  system: System;
  workspaceID: string;
  close: () => void;
  save: (value: Agent) => Promise<void>;
}) {
  const blank = (): Agent => ({
    id: "",
    workspace_id: workspaceID,
    display_name: "",
    handle: "",
    aliases: [],
    role_description: "",
    model: system.models[0],
    tools: allTools,
    disabled: false,
    revision: 1,
  });
  const [value, setValue] = useState<Agent>(agents[0] ?? blank());
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  return (
    <Modal title="成员与称呼" close={close}>
      <div className={cx("member-picker")}>
        {agents.map((agent) => (
          <button
            key={agent.id}
            onClick={() => {
              setValue(agent);
              setSaved(false);
            }}
          >
            {agent.display_name}
          </button>
        ))}
        <button
          onClick={() => {
            setValue(blank());
            setSaved(false);
          }}
        >
          ＋ 新成员
        </button>
      </div>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          setError("");
          void save(value)
            .then(() => setSaved(true))
            .catch((error) => setError(errorText(error)));
        }}
      >
        <label>
          显示名
          <input
            required
            value={value.display_name}
            onChange={(event) => {
              setSaved(false);
              setValue({ ...value, display_name: event.target.value });
            }}
          />
        </label>
        <label>
          Handle
          <input
            required
            pattern="[a-z][a-z0-9-]{1,31}"
            value={value.handle}
            onChange={(event) => {
              setSaved(false);
              setValue({ ...value, handle: event.target.value });
            }}
          />
        </label>
        <label>
          别名（用逗号分隔）
          <input
            value={(value.aliases ?? []).join(",")}
            onChange={(event) => {
              setSaved(false);
              setValue({
                ...value,
                aliases: event.target.value
                  .split(/[,，]/)
                  .map((s) => s.trim())
                  .filter(Boolean),
              });
            }}
          />
        </label>
        <label>
          职责
          <textarea
            value={value.role_description}
            onChange={(event) => {
              setSaved(false);
              setValue({ ...value, role_description: event.target.value });
            }}
          />
        </label>
        <label>
          模型
          <select
            value={value.model}
            onChange={(event) => {
              setSaved(false);
              setValue({ ...value, model: event.target.value });
            }}
          >
            {system.models.map((model) => (
              <option key={model}>{model}</option>
            ))}
          </select>
        </label>
        <fieldset>
          <legend>允许工具</legend>
          {allTools.map((tool) => (
            <label className={cx("check-label")} key={tool}>
              <input
                type="checkbox"
                checked={value.tools?.includes(tool) ?? false}
                onChange={(event) => {
                  setSaved(false);
                  setValue({
                    ...value,
                    tools: event.target.checked
                      ? [...(value.tools ?? []), tool]
                      : (value.tools?.filter((t) => t !== tool) ?? []),
                  });
                }}
              />
              {tool}
            </label>
          ))}
        </fieldset>
        <label className={cx("check-label")}>
          <input
            type="checkbox"
            checked={value.disabled}
            onChange={(event) => {
              setSaved(false);
              setValue({ ...value, disabled: event.target.checked });
            }}
          />
          停用此成员
        </label>
        {error && (
          <p className={cx("error")} role="alert">
            {error}
          </p>
        )}
        {saved && (
          <p role="status">
            已保存。继续编辑前可重新选择该成员以读取最新版本。
          </p>
        )}
        <button className={cx("primary")} disabled={saved}>
          保存成员
        </button>
      </form>
    </Modal>
  );
}

export function ThreadForm({
  current,
  agents,
  close,
  save,
}: {
  current?: Thread;
  agents: Agent[];
  close: () => void;
  save: (value: Thread) => Promise<void>;
}) {
  const [value, setValue] = useState<Thread>(
    current ?? {
      id: "",
      workspace_id: "",
      title: "新会话",
      agent_ids: agents.slice(0, 4).map((a) => a.id),
      coordinator_id: agents[Math.min(2, agents.length - 1)]?.id ?? "",
      workflow: "free",
      revision: 1,
      updated_at: "",
    },
  );
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  return (
    <Modal title={current ? "会话设置" : "新建会话"} close={close}>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          setSaving(true);
          void save(value)
            .catch((error) => setError(errorText(error)))
            .finally(() => setSaving(false));
        }}
      >
        <label>
          会话标题
          <input
            required
            value={value.title}
            onChange={(event) =>
              setValue({ ...value, title: event.target.value })
            }
          />
        </label>
        <fieldset>
          <legend>会话成员（1–4 位）</legend>
          {agents.map((agent) => (
            <label className={cx("check-label")} key={agent.id}>
              <input
                type="checkbox"
                checked={value.agent_ids?.includes(agent.id) ?? false}
                onChange={(event) =>
                  setValue({
                    ...value,
                    agent_ids: event.target.checked
                      ? [...(value.agent_ids ?? []), agent.id]
                      : (value.agent_ids?.filter((id) => id !== agent.id) ??
                        []),
                  })
                }
              />
              {agent.display_name}
            </label>
          ))}
        </fieldset>
        <label>
          协调者
          <select
            value={value.coordinator_id}
            onChange={(event) =>
              setValue({ ...value, coordinator_id: event.target.value })
            }
          >
            {agents
              .filter((agent) => value.agent_ids?.includes(agent.id))
              .map((agent) => (
                <option key={agent.id} value={agent.id}>
                  {agent.display_name}
                </option>
              ))}
          </select>
        </label>
        <label>
          默认工作方式
          <select
            value={value.workflow}
            onChange={(event) =>
              setValue({ ...value, workflow: event.target.value })
            }
          >
            {Object.entries(workflows).map(([key, name]) => (
              <option key={key} value={key}>
                {name}
              </option>
            ))}
          </select>
        </label>
        {error && (
          <p role="alert" className={cx("error")}>
            {error}
          </p>
        )}
        <button className={cx("primary")} disabled={saving}>
          保存会话
        </button>
      </form>
    </Modal>
  );
}
