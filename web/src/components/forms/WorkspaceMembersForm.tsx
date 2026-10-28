import { useState } from "react";
import { api } from "../../api/client";
import type { Agent, WorkspaceMember } from "../../api/client";
import { cx } from "../../styles/classes";
import { errorText, memberLabel } from "../../lib/presentation";
import { Avatar } from "../Avatar";
import { Modal } from "../Modal";
import { allTools } from "./MembersForm";

/**
 * The top member entry manages this workspace's relationships only. Global
 * profile fields and new profiles belong to Settings → Members.
 */
export function WorkspaceMembersForm({
  agents,
  catalog,
  workspaceID,
  refresh,
  close,
}: {
  agents: Agent[];
  catalog: Agent[];
  workspaceID: string;
  refresh: () => Promise<void>;
  close: () => void;
}) {
  const profiles = [
    ...new Set([...agents.map((agent) => agent.id), ...catalog.map((agent) => agent.id)]),
  ].flatMap((id) => {
    const member = agents.find((agent) => agent.id === id);
    const profile = catalog.find((agent) => agent.id === id) ?? member;
    return profile ? [{ profile, member }] : [];
  });
  const [selectedID, setSelectedID] = useState(
    agents[0]?.id ?? catalog[0]?.id ?? "",
  );
  const selected =
    profiles.find(({ profile }) => profile.id === selectedID) ?? profiles[0];
  return (
    <Modal title="工作空间成员" close={close}>
      <p>
        这里只管理本工作空间的成员关系、启用状态和工具授权。全局档案请到设置 →
        成员编辑。
      </p>
      <div className={cx("member-picker")}>
        {profiles.map(({ profile, member }) => (
          <button
            type="button"
            key={profile.id}
            aria-pressed={selected?.profile.id === profile.id}
            onClick={() => setSelectedID(profile.id)}
          >
            <Avatar
              name={profile.display_name}
              id={profile.id}
              avatar={profile.avatar}
              small
            />
            {memberLabel(profile)}
            {member ? " · 已加入" : " · 可加入"}
          </button>
        ))}
      </div>
      {selected && (
        <MemberGrant
          key={`${workspaceID}:${selected.profile.id}`}
          profile={selected.profile}
          membership={selected.member?.membership ?? undefined}
          workspaceID={workspaceID}
          refresh={refresh}
        />
      )}
    </Modal>
  );
}

function MemberGrant({
  profile,
  membership,
  workspaceID,
  refresh,
}: {
  profile: Agent;
  membership?: WorkspaceMember;
  workspaceID: string;
  refresh: () => Promise<void>;
}) {
  const globalTools = profile.tools ?? [];
  const [enabled, setEnabled] = useState(membership?.enabled ?? true);
  const [tools, setTools] = useState(membership?.tools ?? globalTools);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const effective = enabled && !profile.disabled
    ? tools.filter((tool) => globalTools.includes(tool))
    : [];
  const path = `/api/v1/workspaces/${workspaceID}/members/${profile.id}`;
  const submit = (action: () => Promise<void>) => {
    setBusy(true);
    setError("");
    setSaved(false);
    void action()
      .then(refresh)
      .then(() => setSaved(true))
      .catch((error) => setError(errorText(error)))
      .finally(() => setBusy(false));
  };
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        submit(async () => {
          await api(
            path,
            membership ? "PATCH" : "PUT",
            { enabled, tools },
            membership ? { "If-Match": String(membership.revision) } : {},
          );
        });
      }}
    >
      <fieldset disabled={busy}>
        <legend>本工作空间权限</legend>
        <label className={cx("check-label")}>
          <input
            type="checkbox"
            checked={enabled}
            onChange={(event) => {
              setSaved(false);
              setEnabled(event.target.checked);
            }}
          />
          在本工作空间启用
        </label>
        {allTools.map((tool) => {
          const allowed = globalTools.includes(tool);
          const checked = tools.includes(tool);
          return (
            <label className={cx("check-label")} key={tool}>
              <input
                type="checkbox"
                checked={checked}
                disabled={!allowed && !checked}
                onChange={(event) => {
                  setSaved(false);
                  setTools(
                    event.target.checked
                      ? [...tools, tool]
                      : tools.filter((item) => item !== tool),
                  );
                }}
              />
              {tool}
              {!allowed && "（全局未允许）"}
            </label>
          );
        })}
      </fieldset>
      <p>
        实际权限取本空间与全局配置的交集。当前实际生效：
        {effective.length ? effective.join("、") : "无"}。
        移出仅撤销本空间访问，保留全局成员档案和历史记录。
      </p>
      {error && (
        <p role="alert" className={cx("error")}>
          {error}
        </p>
      )}
      {saved && <p role="status">本空间成员配置已保存。</p>}
      <div className={cx("form-actions")}>
        {membership && (
          <button
            type="button"
            disabled={busy}
            onClick={() =>
              submit(async () => {
                await api(path, "DELETE", undefined, {
                  "If-Match": String(membership.revision),
                });
              })
            }
          >
            移出本工作空间
          </button>
        )}
        <button className={cx("primary")} disabled={busy}>
          {busy ? "保存中…" : membership ? "保存本空间权限" : "加入本工作空间"}
        </button>
      </div>
    </form>
  );
}
