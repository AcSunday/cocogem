import { Select } from "../Select";
import { useId, useRef, useState } from "react";
import type { Workspace, System } from "../../api/client";
import { t, useLanguage } from "../../lib/i18n";
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

const cx = scoped(base, controls, layout, conversation, panels, members, settings);

export function WorkspaceForm({
  current,
  system,
  close,
  save,
  onDelete,
}: {
  current?: Workspace;
  system: System;
  close: () => void;
  save: (value: Workspace) => Promise<void>;
  onDelete?: () => void;
}) {
  useLanguage();
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
    <Modal
      title={
        current
          ? t("编辑工作空间", "Edit Project")
          : t("创建工作空间", "New Project")
      }
      close={close}
    >
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
          {t("名称", "Name")}
          <input
            autoFocus
            required
            maxLength={100}
            value={value.name}
            onChange={(event) =>
              setValue({ ...value, name: event.target.value })
            }
            placeholder={t("例如：我的研究室", "e.g., Research Lab")}
          />
        </label>
        <label>
          {t("说明", "Description")}
          <textarea
            value={value.description}
            onChange={(event) =>
              setValue({ ...value, description: event.target.value })
            }
            placeholder={t(
              "这个空间想一起完成什么？",
              "What do you want to accomplish in this project?",
            )}
          />
        </label>
        <div className={cx("directory-location")}>
          <label>
            {t("本地资料目录", "Local project directory")}
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
              <option value="">{t("暂不绑定", "None (unbound)")}</option>
              {system.roots.map((root) => (
                <option key={root}>{root}</option>
              ))}
              <option value="custom-root">
                {t("输入目录路径…", "Enter directory path…")}
              </option>
            </Select>
          </label>
          <button
            type="button"
            ref={browseButton}
            aria-expanded={browsing}
            disabled={saving}
            onClick={() => setBrowsing((current) => !current)}
          >
            {t("浏览目录", "Browse")}
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
              {t("目录路径", "Directory path")}
              <input
                disabled={saving || browsing}
                value={value.root}
                maxLength={4096}
                spellCheck={false}
                autoComplete="off"
                aria-describedby={rootHelpID}
                placeholder={t(
                  "例如：/完整路径/项目目录",
                  "e.g., /full/path/to/project",
                )}
                onChange={(event) => {
                  setError("");
                  setValue({ ...value, root: event.target.value });
                }}
              />
            </label>
            <small id={rootHelpID}>
              {t(
                "填写本机文件夹的完整路径，支持 ~/。",
                "Enter the full path to a local folder (supports ~/). ",
              )}
              {system.roots.length > 0
                ? t(
                    "请选择上列允许目录或其子目录。",
                    "Choose one of the allowed roots above or a subdirectory.",
                  )
                : t(
                    "保存时会检查目录是否存在、可读。",
                    "Verified for existence and read access on save.",
                  )}
            </small>
          </>
        )}
        {!!skills.length && (
          <fieldset className={cx("workspace-skills")}>
            <legend>{t("共享协作技能", "Shared collaboration skills")}</legend>
            <p className={cx("muted")}>
              {t(
                "成员按任务需要加载技能，具体分工根据当前会话成员决定。",
                "Members load skills on demand based on the task and current conversation roster.",
              )}
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
          {saving
            ? t("保存中…", "Saving…")
            : t("保存工作空间", "Save project")}
        </button>
      </div>
      {onDelete && (
        <section className={cx("danger-zone")}>
          <p className={cx("muted")}>
            {t(
              "删除后，此空间的成员关联、会话、来源和产物都会被永久移除。全局成员档案会保留。",
              "Deleting permanently removes this project's member links, conversations, sources, and deliverables. Global member profiles are kept.",
            )}
          </p>
          <button type="button" className={cx("danger")} onClick={onDelete}>
            {t("删除工作空间", "Delete project")}
          </button>
        </section>
      )}
    </Modal>
  );
}
