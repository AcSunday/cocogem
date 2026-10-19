import { useState } from "react";
import type { Workspace, System } from "../../api/client";
import { cx } from "../../styles/classes";
import { errorText } from "../../lib/presentation";
import { Modal } from "../Modal";

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
          我理解真实模式会通过所选本机 CLI
          将问题和资料片段发送至其模型服务；搜索会发送查询词。
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
