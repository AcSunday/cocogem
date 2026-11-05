import { Select } from "../Select";
import { useId, useRef, useState } from "react";
import type { Workspace, System, Settings, Agent } from "../../api/client";
import { scoped } from "../../styles/classes";
import base from "../../styles/base.module.css";
import controls from "../../styles/controls.module.css";
import layout from "../../styles/layout.module.css";
import conversation from "../../styles/conversation.module.css";
import panels from "../../styles/panels.module.css";
import members from "../../styles/member-editor.module.css";
import settings from "../../styles/settings.module.css";
import { errorText, skillLabel } from "../../lib/presentation";
import { Modal } from "../Modal";
import { DirectoryPicker } from "../DirectoryPicker";
import { ProjectSettings } from "../ProjectSettings";

const cx = scoped(base, controls, layout, conversation, panels, members, settings);

export function WorkspaceForm({
  current,
  system,
  settings,
  agents = [],
  close,
  save,
  onDelete,
  onWorkspaceChange,
}: {
  current?: Workspace;
  system: System;
  settings?: Settings;
  agents?: Agent[];
  close: () => void;
  save: (value: Workspace) => Promise<void>;
  onDelete?: () => void;
  onWorkspaceChange?: (workspace: Workspace) => void;
}) {
  const [value, setValue] = useState<Workspace>(
    current ?? {
      id: "",
      name: "",
      description: "",
      root: "",
      revision: 1,
      created_at: "",
      skill_ids: null,
    },
  );
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [browsing, setBrowsing] = useState(false);
  const browseButton = useRef<HTMLButtonElement>(null);
  const [customRoot, setCustomRoot] = useState(
    !!current?.root && !system.roots.includes(current.root),
  );
  const rootHelpID = useId();
  const skills = system.skills ?? [];
  const enabledSkills = value.skill_ids ?? skills.map((skill) => skill.id);
  return (
    <Modal title={current ? "编辑工作空间" : "创建工作空间"} close={close}>
      <form
        id="workspace-form"
        onSubmit={(event) => {
          event.preventDefault();
          if (saving || browsing) return;
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
        <div className={cx("directory-location")}>
          <label>
            本地资料目录
            <Select
              disabled={saving || browsing}
              value={customRoot ? "custom-root" : value.root}
              onChange={(selected) => {
                setError("");
                setCustomRoot(selected === "custom-root");
                if (selected !== "custom-root")
                  setValue({ ...value, root: selected });
              }}
            >
              <option value="">暂不绑定</option>
              {system.roots.map((root) => (
                <option key={root}>{root}</option>
              ))}
              <option value="custom-root">输入目录路径…</option>
            </Select>
          </label>
          <button
            type="button"
            ref={browseButton}
            aria-expanded={browsing}
            disabled={saving}
            onClick={() => setBrowsing((current) => !current)}
          >
            浏览目录
          </button>
        </div>
        {browsing && (
          <DirectoryPicker
            initialPath={value.root}
            restricted={system.roots.length > 0}
            close={() => {
              setBrowsing(false);
              browseButton.current?.focus();
            }}
            choose={(root) => {
              setValue({ ...value, root });
              setCustomRoot(!system.roots.includes(root));
              setError("");
              setBrowsing(false);
              browseButton.current?.focus();
            }}
          />
        )}
        {customRoot && (
          <>
            <label>
              目录路径
              <input
                disabled={saving || browsing}
                value={value.root}
                maxLength={4096}
                spellCheck={false}
                autoComplete="off"
                aria-describedby={rootHelpID}
                placeholder="例如：/完整路径/项目目录"
                onChange={(event) => {
                  setError("");
                  setValue({ ...value, root: event.target.value });
                }}
              />
            </label>
            <small id={rootHelpID}>
              填写本机文件夹的完整路径，支持 ~/。
              {system.roots.length > 0
                ? "请选择上列允许目录或其子目录。"
                : "保存时会检查目录是否存在、可读。"}
            </small>
          </>
        )}
        {!!skills.length && (
          <fieldset className={cx("workspace-skills")}>
            <legend>共享协作技能</legend>
            <p className={cx("muted")}>
              成员按任务读取技能，具体分工根据本轮成员名册决定。
            </p>
            {skills.map((skill) => (
              <label className={cx("check-label")} key={skill.id}>
                <input
                  type="checkbox"
                  checked={enabledSkills.includes(skill.id)}
                  onChange={(event) =>
                    setValue({
                      ...value,
                      skill_ids: event.target.checked
                        ? [...enabledSkills, skill.id]
                        : enabledSkills.filter((id) => id !== skill.id),
                    })
                  }
                />
                <span>
                  {skillLabel(skill.id)}
                  <small>{skill.description}</small>
                </span>
              </label>
            ))}
          </fieldset>
        )}
      </form>
      {current && (
        <section className={cx("workspace-permissions")}>
          <p className={cx("muted")}>
            权限、持续授权和新建根任务的预算默认值属于整个项目。保存后立即生效，既有 Run 继续使用冻结快照。
          </p>
          <ProjectSettings
            workspace={value}
            settings={settings}
            agents={agents}
            onWorkspaceChange={(updated) => {
              setValue((current) => ({
                ...current,
                revision: updated.revision,
                execution_mode: updated.execution_mode,
                budget_override: updated.budget_override,
              }));
              onWorkspaceChange?.(updated);
            }}
          />
        </section>
      )}
      {error && (
        <p className={cx("error")} role="alert">
          {error}
        </p>
      )}
      <div className={cx("form-actions workspace-save-actions")}>
        <button
          form="workspace-form"
          className={cx("primary")}
          disabled={saving || browsing}
        >
          {saving ? "保存中…" : "保存工作空间"}
        </button>
      </div>
      {onDelete && (
        <section className={cx("danger-zone")}>
          <p className={cx("muted")}>
            删除后，此空间的成员关联、会话、来源和产物都会被永久移除。全局成员档案会保留。
          </p>
          <button type="button" className={cx("danger")} onClick={onDelete}>
            删除工作空间
          </button>
        </section>
      )}
    </Modal>
  );
}
