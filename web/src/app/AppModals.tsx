import type {
  Agent,
  Artifact,
  Run,
  Settings,
  Source,
  System,
  Thread,
  ThreadView,
  Workspace,
} from "../api/client";
import type { RunInput } from "../api/client";
import type { WorkflowKey } from "../components/Composer";
import { ArtifactReader } from "../components/ArtifactReader";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { SettingsDialog, SnapshotDialog } from "../components/SettingsDialog";
import { ThreadForm } from "../components/forms/ThreadForm";
import { ThreadRenameForm } from "../components/forms/ThreadRenameForm";
import { WorkspaceForm } from "../components/forms/WorkspaceForm";
import { WorkspaceMembersForm } from "../components/forms/WorkspaceMembersForm";
import { api } from "../api/client";
import type { Appearance } from "../components/AppearanceSelect";
import { errorText } from "../lib/presentation";

export type ModalState =
  | { kind: "workspace" | "edit-workspace" | "agents" | "settings" }
  | { kind: "thread" }
  | { kind: "edit-thread"; thread?: Thread }
  | { kind: "snapshot"; run: Run }
  | { kind: "artifact" }
  | { kind: "rename-thread"; thread: Thread }
  | { kind: "delete-thread"; thread: Thread }
  | { kind: "delete-workspace"; workspace: Workspace };

/**
 * Every dialog the shell can open. Rendering is exclusive by construction:
 * `modal` holds a single state, so two dialogs can never stack.
 */
export function AppModals({
  modal,
  close,
  system,
  setSystem,
  workspace,
  workspaceID,
  threadID,
  agents,
  globalAgents,
  view,
  settings,
  setSettings,
  artifact,
  threads,
  lock,
  navigate,
  loadSpaces,
  loadMembers,
  loadThreads,
  loadGlobalAgents,
  setView,
  setWorkflowChoices,
  setArtifactRef,
  setPanelOpen,
  setError,
  openSource,
  saveFeedback,
  pendingSubmission,
  appearance,
  onAppearance,
  setModal,
}: {
  modal: ModalState | null;
  setModal: (value: ModalState | null) => void;
  close: () => void;
  system?: System;
  setSystem: (value: System) => void;
  workspace?: Workspace;
  workspaceID: string;
  threadID: string;
  agents: Agent[];
  globalAgents: Agent[];
  view?: ThreadView;
  settings?: Settings;
  setSettings: (value: Settings) => void;
  artifact?: Artifact;
  threads: Thread[];
  lock: () => Promise<void>;
  navigate: (ws: string, th?: string) => void;
  loadSpaces: () => Promise<Workspace[]>;
  loadMembers: (id: string) => Promise<void>;
  loadThreads: (id: string) => Promise<void>;
  loadGlobalAgents: () => Promise<void>;
  setView: (
    update: (current: ThreadView | undefined) => ThreadView | undefined,
  ) => void;
  setWorkflowChoices: (
    update: (
      current: Record<string, WorkflowKey>,
    ) => Record<string, WorkflowKey>,
  ) => void;
  setArtifactRef: (value?: Artifact) => void;
  setPanelOpen: (value: boolean) => void;
  setError: (value: string) => void;
  openSource: (value: Source | string) => void;
  saveFeedback: (value: string) => Promise<void>;
  pendingSubmission: Map<string, { input: RunInput; key: string; threadID: string }>;
  appearance: Appearance;
  onAppearance: (value: Appearance) => void;
}) {
  return (
    <>
      {(modal?.kind === "workspace" || modal?.kind === "edit-workspace") &&
        system && (
          <WorkspaceForm
            current={modal.kind === "edit-workspace" ? workspace : undefined}
            system={system}
            settings={settings}
            close={close}
            onWorkspaceChange={() => void loadSpaces()}
            onDelete={
              modal.kind === "edit-workspace" && workspace
                ? () => setModal({ kind: "delete-workspace", workspace })
                : undefined
            }
            save={async (value) => {
              const saved = await api<Workspace>(
                value.id
                  ? `/api/v1/workspaces/${value.id}`
                  : "/api/v1/workspaces",
                value.id ? "PATCH" : "POST",
                {
                  name: value.name,
                  description: value.description,
                  root: value.root,
                  skill_ids: value.skill_ids,
                },
                value.id ? { "If-Match": String(value.revision) } : {},
              );
              await loadSpaces();
              close();
              if (!value.id) navigate(saved.id);
            }}
          />
        )}
      {modal?.kind === "agents" && workspaceID && (
        <WorkspaceMembersForm
          agents={agents}
          catalog={globalAgents}
          workspaceID={workspaceID}
          refresh={() => loadMembers(workspaceID)}
          close={close}
        />
      )}
      {(modal?.kind === "thread" || modal?.kind === "edit-thread") &&
        workspace && (
          <ThreadForm
            current={
              modal.kind === "edit-thread"
                ? (modal.thread ?? view?.thread)
                : undefined
            }
            agents={agents.filter((agent) => !agent.disabled)}
            defaultCoordinatorID={settings?.default_coordinator_id}
            close={close}
            save={async (value) => {
              const th = await api<Thread>(
                value.id
                  ? `/api/v1/threads/${value.id}`
                  : `/api/v1/workspaces/${workspaceID}/threads`,
                value.id ? "PATCH" : "POST",
                {
                  title: value.title,
                  agent_ids: value.agent_ids,
                  coordinator_id: value.coordinator_id,
                  workflow: value.workflow,
                },
                value.id ? { "If-Match": String(value.revision) } : {},
              );
              setWorkflowChoices((current) => ({
                ...current,
                [th.id]: th.workflow as WorkflowKey,
              }));
              if (value.id)
                setView((current) =>
                  current?.thread.id === th.id
                    ? { ...current, thread: th }
                    : current,
                );
              close();
              await loadThreads(workspaceID);
              if (!value.id) navigate(workspaceID, th.id);
            }}
          />
        )}
      {modal?.kind === "snapshot" && (
        <SnapshotDialog run={modal.run} close={close} />
      )}
      {modal?.kind === "settings" && system && (
        <SettingsDialog
          system={system}
          setSystem={setSystem}
          close={close}
          lock={lock}
          settings={settings}
          saveSettings={async (value) => {
            const saved = await api<Settings>(
              "/api/v1/settings",
              "PATCH",
              value,
              { "If-Match": String(settings?.revision ?? 0) },
            );
            setSettings(saved);
            return saved;
          }}
          agents={globalAgents}
          saveAgent={async (value) => {
            const saved = await api<Agent>(
              value.id ? `/api/v1/agents/${value.id}` : "/api/v1/agents",
              value.id ? "PATCH" : "POST",
              value,
              value.id ? { "If-Match": String(value.revision) } : {},
            );
            await loadGlobalAgents();
            if (workspaceID) await loadMembers(workspaceID);
            return saved;
          }}
          onRestoreThread={async (thread) => {
            close();
            navigate(thread.workspace_id, thread.id);
            await loadThreads(thread.workspace_id);
          }}
          onTrashChanged={() => loadThreads(workspaceID)}
          appearance={appearance}
          onAppearance={onAppearance}
        />
      )}
      {modal?.kind === "artifact" && artifact && (
        <ArtifactReader
          artifact={artifact}
          sources={view?.sources ?? []}
          close={close}
          onFeedback={(value) => void saveFeedback(value)}
          onReference={() => {
            setArtifactRef(artifact);
            close();
            setPanelOpen(false);
            requestAnimationFrame(() =>
              document.getElementById("composer-input")?.focus(),
            );
          }}
          onSource={(id) => {
            close();
            openSource(id);
          }}
          onError={(error) => setError(errorText(error))}
        />
      )}
      {modal?.kind === "rename-thread" && (
        <ThreadRenameForm
          thread={modal.thread}
          close={close}
          save={async (title) => {
            // The title-only update leaves members, coordinator and workflow untouched.
            const saved = await api<Thread>(
              `/api/v1/threads/${modal.thread.id}`,
              "PATCH",
              { title },
              { "If-Match": String(modal.thread.revision) },
            );
            setView((current) =>
              current?.thread.id === saved.id
                ? { ...current, thread: saved }
                : current,
            );
            close();
            await loadThreads(workspaceID);
          }}
        />
      )}
      {modal?.kind === "delete-thread" && (
        <ConfirmDialog
          title="删除会话"
          action="删除会话"
          close={close}
          confirm={async () => {
            await api(`/api/v1/threads/${modal.thread.id}`, "DELETE");
            close();
            pendingSubmission.delete(modal.thread.id);
            await loadThreads(workspaceID);
            if (modal.thread.id === threadID) navigate(workspaceID);
          }}
        >
          <p>
            “{modal.thread.title}
            ”会被移入回收站，保留 {settings?.retention_days ?? 7}{" "}
            天。期间可在“设置 → 回收站”恢复；到期后自动清理。
          </p>
        </ConfirmDialog>
      )}
      {modal?.kind === "delete-workspace" && (
        <ConfirmDialog
          title="删除工作空间"
          action="删除工作空间"
          close={close}
          confirm={async () => {
            await api(`/api/v1/workspaces/${modal.workspace.id}`, "DELETE");
            close();
            const spaces = await loadSpaces();
            navigate(spaces[0]?.id ?? "");
          }}
        >
          <p>
            “{modal.workspace.name}”及其中的 {threads.length}{" "}
            个会话、成员关联、来源和产物会被永久删除，无法恢复。全局成员档案会保留。
          </p>
        </ConfirmDialog>
      )}
    </>
  );
}
