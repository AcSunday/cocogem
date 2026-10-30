import { useState } from "react";
import type { Agent, System, WorkspaceMember } from "../../api/client";
import { cx } from "../../styles/classes";
import {
  errorText,
  getMemberColorId,
  memberLabel,
  memberPalette,
  setMemberColorId,
} from "../../lib/presentation";
import { Avatar, avatars } from "../Avatar";
import { Modal } from "../Modal";
import { Select } from "../Select";

const readOnlyTools = [
  "list_files",
  "read_file",
  "search_content",
  "web_search",
  "fetch_url",
  "read_source",
  "publish_artifact",
];
const engineeringTools = ["write_file", "patch_file", "run_test"];
export const allTools = [...readOnlyTools, ...engineeringTools];
const names = (value: string) =>
  value
    .split(/[,，]/)
    .map((item) => item.trim())
    .filter(Boolean);

export function MembersForm({
  agents,
  system,
  workspaceID,
  catalog,
  global,
  embedded,
  saveMembership,
  unlink,
  close,
  save,
}: {
  agents: Agent[];
  system: System;
  workspaceID: string;
  catalog?: Agent[];
  global?: boolean;
  embedded?: boolean;
  saveMembership?: (
    agent: Agent,
    member: WorkspaceMember | undefined,
    value: { enabled: boolean; tools: string[] },
  ) => Promise<void>;
  unlink?: (member: WorkspaceMember) => Promise<void>;
  close: () => void;
  save: (value: Agent) => Promise<Agent>;
}) {
  const executor =
    system.executors?.find((item) => item.installed && item.supported !== false)
      ?.id ?? "claude";
  const blank = (): Agent => ({
    id: "",
    workspace_id: workspaceID,
    display_name: "",
    handle: "",
    aliases: [],
    role_description: "",
    executor,
    model: "default",
    tools: readOnlyTools,
    disabled: false,
    revision: 1,
  });
  
  const [editingAgent, setEditingAgent] = useState<Agent | null>(null);

  const executorInfo = system.executors?.find(
    (item) => item.id === editingAgent?.executor,
  );
  
  const body = editingAgent ? (
    <MemberEditor
      agent={editingAgent}
      system={system}
      catalog={catalog}
      global={global}
      save={save}
      onBack={() => setEditingAgent(null)}
    />
  ) : (
    <div className={cx("member-list")}>
      {(catalog ?? agents).map((agent) => (
        <button
          key={agent.id}
          className={cx("member-card")}
          onClick={() => setEditingAgent(agent)}
        >
          <Avatar
            name={agent.display_name}
            id={agent.id}
            avatar={agent.avatar}
          />
          <div className={cx("member-info")}>
            <strong>{memberLabel(agent)}</strong>
            <small>{agent.role_description || "无职责描述"}</small>
          </div>
        </button>
      ))}
      <button
        className={cx("member-card add-member")}
        onClick={() => setEditingAgent(blank())}
      >
        ＋ 新成员
      </button>
    </div>
  );

  return embedded ? (
    body
  ) : (
    <Modal title="成员与称呼" close={close} wide>
      {body}
    </Modal>
  );
}

function MemberEditor({
  agent: initialAgent,
  system,
  catalog,
  global,
  save,
  onBack,
}: {
  agent: Agent;
  system: System;
  catalog?: Agent[];
  global?: boolean;
  save: (value: Agent) => Promise<Agent>;
  onBack: () => void;
}) {
  const [value, setValue] = useState<Agent>(initialAgent);
  const [colorId, setColorId] = useState<string>(() =>
    initialAgent.id ? getMemberColorId(initialAgent.id) : memberPalette[0].id,
  );
  const [aliases, setAliases] = useState((initialAgent.aliases ?? []).join(","));
  const [capabilities, setCapabilities] = useState(
    (initialAgent.capabilities ?? []).join(","),
  );
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const [saving, setSaving] = useState(false);

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        setError("");
        setSaving(true);
        void save({
          ...value,
          aliases: names(aliases),
          ...(catalog ? { capabilities: names(capabilities) } : {}),
        })
          .then((agent) => {
            if (agent.id) setMemberColorId(agent.id, colorId);
            setValue(agent);
            setSaved(true);
          })
          .catch((error) => setError(errorText(error)))
          .finally(() => setSaving(false));
      }}
    >
      <button type="button" onClick={onBack}>← 返回列表</button>
      
      <label>
        显示名
        <input
          required
          value={value.display_name}
          onChange={(event) => setValue({ ...value, display_name: event.target.value })}
        />
      </label>

      <label>
        专属颜色
        <div className={cx("color-picker")}>
          {memberPalette.map((item) => (
            <button
              key={item.id}
              type="button"
              className={cx(`color-dot ${colorId === item.id ? "selected" : ""}`)}
              style={{ backgroundColor: item.color }}
              onClick={() => setColorId(item.id)}
            />
          ))}
        </div>
      </label>

      <label>
        头像
        <Select
          value={value.avatar ?? ""}
          onChange={(selected) => setValue({ ...value, avatar: selected })}
        >
          <option value="">使用名称首字</option>
          {avatars.map((item) => (
            <option key={item} value={item}>
              {item}
            </option>
          ))}
        </Select>
      </label>

      <div className={cx("form-actions")}>
        <button type="submit" className={cx("primary")} disabled={saving}>
          {saving ? "保存中…" : "保存成员"}
        </button>
      </div>
    </form>
  );
}
