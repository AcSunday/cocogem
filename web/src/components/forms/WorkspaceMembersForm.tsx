import { useState } from "react";
import { api } from "../../api/client";
import type { Agent, WorkspaceMember } from "../../api/client";
import { scoped } from "../../styles/classes";
import base from "../../styles/base.module.css";
import controls from "../../styles/controls.module.css";
import conversation from "../../styles/conversation.module.css";
import panels from "../../styles/panels.module.css";
import members from "../../styles/member-editor.module.css";
import settings from "../../styles/settings.module.css";
import { errorText, memberLabel } from "../../lib/presentation";
import { Avatar } from "../Avatar";
import { Modal } from "../Modal";

const cx = scoped(base, controls, conversation, panels, members, settings);

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
        这里只管理本工作空间的成员关系与启用状态。成员的工具与命令边界由设置中的全局权限决定；全局档案请到设置 → 成员编辑。
      </p>
      <div className={cx("member-picker")}>
        {profiles.map(({ profile, member }) => (
          <button
            type="button"
            key={profile.id}
            aria-label={`${memberLabel(profile)} · ${member ? "已加入" : "可加入"}`}
            aria-pressed={selected?.profile.id === profile.id}
            onClick={() => setSelectedID(profile.id)}
          >
            <Avatar
              name={profile.display_name}
              avatar={profile.avatar}
              small
            />
            <span className={cx("member-picker-identity")}>
              <strong title={profile.display_name}>{profile.display_name}</strong>
              <small title={`@${profile.handle}`}>@{profile.handle}</small>
            </span>
            <span className={cx("member-picker-status")}>
              {member ? "已加入" : "可加入"}
            </span>
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
  const [enabled, setEnabled] = useState(membership?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
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
            // Field merge keeps the stored legacy tool array untouched; the
            // executed boundary no longer depends on it.
            { enabled, ...(membership ? {} : { tools: null }) },
            membership ? { "If-Match": String(membership.revision) } : {},
          );
        });
      }}
    >
      <fieldset disabled={busy}>
        <legend>本工作空间成员</legend>
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
      </fieldset>
      <p>
        {profile.disabled
          ? "该成员已在全局停用，加入本空间也不会执行。"
          : "成员的工具与命令边界由设置中的全局权限决定。"}
        移出仅撤销本空间访问，保留全局成员档案和历史记录。
      </p>
      {error && (
        <p role="alert" className={cx("error")}>
          {error}
        </p>
      )}
      {saved && <p role="status">本空间成员设置已保存。</p>}
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
          {busy ? "保存中…" : membership ? "保存本空间设置" : "加入本工作空间"}
        </button>
      </div>
    </form>
  );
}
